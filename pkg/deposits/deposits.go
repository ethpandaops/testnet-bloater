package deposits

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/testnet-bloater/pkg/tracker"
	hbls "github.com/herumi/bls-eth-go-binary/bls"
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/util/hashing"
	"github.com/protolambda/ztyp/tree"
	"github.com/sirupsen/logrus"
	"github.com/tyler-smith/go-bip39"
	e2types "github.com/wealdtech/go-eth2-types/v2"
	util "github.com/wealdtech/go-eth2-util"
)

func init() {
	hbls.Init(hbls.BLS12_381)
	hbls.SetETHmode(hbls.EthModeLatest)
}

// Config holds deposit generation configuration.
type Config struct {
	Mnemonic             string
	DepositContract      ethcommon.Address
	DepositAmount        uint64 // gwei
	StartIndex           uint64
	Count                uint64
	BatchSize            uint64
	StorageInterval      uint64
	GenesisForkVersion   string            // hex, e.g. "0x00000000"
	WithdrawalCredPrefix byte              // 0x00 (BLS), 0x01 (execution), 0x02
	WithdrawalAddress    ethcommon.Address // used when prefix is 0x01 or 0x02
}

// Runner generates and submits deposit transactions.
type Runner struct {
	log     logrus.FieldLogger
	cfg     Config
	tracker *tracker.Tracker
	seed    []byte
}

// NewRunner creates a deposit runner.
func NewRunner(
	log logrus.FieldLogger,
	cfg Config,
	trk *tracker.Tracker,
) (*Runner, error) {
	mnemonic := strings.TrimSpace(cfg.Mnemonic)
	if !bip39.IsMnemonicValid(mnemonic) {
		return nil, errors.New("mnemonic is not valid")
	}
	seed := bip39.NewSeed(mnemonic, "")

	// Initialize BLS types
	if err := e2types.InitBLS(); err != nil {
		// May already be initialized, ignore
		_ = err
	}

	return &Runner{
		log:     log.WithField("package", "deposits"),
		cfg:     cfg,
		tracker: trk,
		seed:    seed,
	}, nil
}

// Run generates and submits deposit transactions in multicall batches.
// Returns the number of deposits successfully submitted.
func (r *Runner) Run(ctx context.Context) (uint64, error) {
	r.log.Infof(
		"starting deposits: startIndex=%d count=%d amount=%d gwei",
		r.cfg.StartIndex, r.cfg.Count, r.cfg.DepositAmount,
	)

	// Parse genesis fork version for domain computation
	forkVersionBytes := ethcommon.FromHex(r.cfg.GenesisForkVersion)
	var forkVersion common.Version
	copy(forkVersion[:], forkVersionBytes)

	// Read current state
	state, err := r.tracker.GetValues(ctx, tracker.DepositKeys())
	if err != nil {
		return 0, fmt.Errorf("failed to read tracker state: %w", err)
	}

	dailyTimestamp := state[tracker.KeyDailyDepositTimestamp]
	if dailyTimestamp == 0 {
		dailyTimestamp = uint64(time.Now().Unix())
	}

	var submitted uint64
	var batch []tracker.Call

	for i := uint64(0); i < r.cfg.Count; i++ {
		select {
		case <-ctx.Done():
			return submitted, ctx.Err()
		default:
		}

		accountIdx := r.cfg.StartIndex + i

		call, err := r.generateDepositCall(accountIdx, forkVersion)
		if err != nil {
			return submitted, fmt.Errorf(
				"failed to generate deposit for index %d: %w",
				accountIdx, err,
			)
		}

		batch = append(batch, *call)
		r.log.Infof(
			"prepared deposit %d/%d (index: %d)",
			i+1, r.cfg.Count, accountIdx,
		)

		// Send batch via multicall
		if uint64(len(batch)) >= r.cfg.BatchSize || i == r.cfg.Count-1 {
			batchLen := uint64(len(batch))
			newSubmitted := submitted + batchLen

			// Always append storage update to keep on-chain state current
			{
				stateCall, err := r.tracker.SetValuesCall(map[string]uint64{
					tracker.KeyTotalDeposits:         state[tracker.KeyTotalDeposits] + newSubmitted,
					tracker.KeyLastDepositIndex:      r.cfg.StartIndex + newSubmitted,
					tracker.KeyDailyDepositCount:     state[tracker.KeyDailyDepositCount] + newSubmitted,
					tracker.KeyDailyDepositTimestamp: dailyTimestamp,
				})
				if err != nil {
					r.log.Warnf("failed to build state update call: %v", err)
				} else {
					batch = append(batch, *stateCall)
				}
			}

			r.log.Infof("submitting multicall batch of %d deposits (+ state update)...", batchLen)
			err := r.tracker.Multicall(ctx, batch, 200000)
			if err != nil {
				return submitted, fmt.Errorf(
					"multicall batch failed: %w", err,
				)
			}
			submitted = newSubmitted
			batch = batch[:0]
		}
	}

	return submitted, nil
}

// generateDepositCall builds the calldata and value for a deposit
// call, returning a tracker.Call for use in a multicall batch.
func (r *Runner) generateDepositCall(
	accountIdx uint64,
	forkVersion common.Version,
) (*tracker.Call, error) {
	// 1. Derive validator signing key
	validatorKeyPath := fmt.Sprintf("m/12381/3600/%d/0/0", accountIdx)
	validatorPrivkey, err := util.PrivateKeyFromSeedAndPath(
		r.seed, validatorKeyPath,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"failed generating validator key %s: %w",
			validatorKeyPath, err,
		)
	}
	validatorPubkey := validatorPrivkey.PublicKey().Marshal()

	// 2. Build withdrawal credentials
	var withdrCreds []byte
	if r.cfg.WithdrawalCredPrefix == 0x01 || r.cfg.WithdrawalCredPrefix == 0x02 {
		// Execution layer credentials: prefix + 11 zero bytes + 20-byte address
		withdrCreds = make([]byte, 32)
		withdrCreds[0] = r.cfg.WithdrawalCredPrefix
		copy(withdrCreds[12:], r.cfg.WithdrawalAddress[:])
	} else {
		// BLS credentials (0x00): hash of withdrawal pubkey
		withdrAccPath := fmt.Sprintf("m/12381/3600/%d/0", accountIdx)
		withdrPrivkey, err := util.PrivateKeyFromSeedAndPath(
			r.seed, withdrAccPath,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"failed generating withdrawal key %s: %w",
				withdrAccPath, err,
			)
		}
		withdrPubKey := withdrPrivkey.PublicKey().Marshal()
		withdrKeyHash := hashing.Hash(withdrPubKey)
		withdrCreds = withdrKeyHash[:]
		withdrCreds[0] = common.BLS_WITHDRAWAL_PREFIX
	}

	// 3. Build deposit data
	var pub common.BLSPubkey
	copy(pub[:], validatorPubkey)

	depositData := common.DepositData{
		Pubkey:                pub,
		WithdrawalCredentials: tree.Root(withdrCreds),
		Amount:                common.Gwei(r.cfg.DepositAmount),
		Signature:             common.BLSSignature{},
	}

	// 4. BLS sign
	msgRoot := depositData.ToMessage().HashTreeRoot(tree.GetHashFn())

	var secKey hbls.SecretKey
	err = secKey.Deserialize(validatorPrivkey.Marshal())
	if err != nil {
		return nil, fmt.Errorf("cannot convert validator priv key: %w", err)
	}

	dom := common.ComputeDomain(
		common.DOMAIN_DEPOSIT, forkVersion, common.Root{},
	)
	msg := common.ComputeSigningRoot(msgRoot, dom)
	sig := secKey.SignHash(msg[:])
	copy(depositData.Signature[:], sig.Serialize())

	dataRoot := depositData.HashTreeRoot(tree.GetHashFn())

	// 5. Build deposit calldata
	depositContractABI := `[{"inputs":[{"internalType":"bytes","name":"pubkey","type":"bytes"},{"internalType":"bytes","name":"withdrawal_credentials","type":"bytes"},{"internalType":"bytes","name":"signature","type":"bytes"},{"internalType":"bytes32","name":"deposit_data_root","type":"bytes32"}],"name":"deposit","outputs":[],"stateMutability":"payable","type":"function"}]`

	parsedABI, err := abi.JSON(strings.NewReader(depositContractABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse deposit ABI: %w", err)
	}

	calldata, err := parsedABI.Pack(
		"deposit",
		depositData.Pubkey[:],
		depositData.WithdrawalCredentials[:],
		depositData.Signature[:],
		dataRoot,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to pack deposit call: %w", err)
	}

	// Amount in wei = gwei * 1e9
	amountWei := new(big.Int).SetUint64(r.cfg.DepositAmount)
	amountWei.Mul(amountWei, big.NewInt(1000000000))

	return &tracker.Call{
		Target:   r.cfg.DepositContract,
		Calldata: calldata,
		Value:    amountWei,
	}, nil
}

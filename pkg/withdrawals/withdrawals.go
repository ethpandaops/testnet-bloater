package withdrawals

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/testnet-bloater/pkg/tracker"
	hbls "github.com/herumi/bls-eth-go-binary/bls"
	"github.com/sirupsen/logrus"
	"github.com/tyler-smith/go-bip39"
	e2types "github.com/wealdtech/go-eth2-types/v2"
	util "github.com/wealdtech/go-eth2-util"
)

func init() {
	hbls.Init(hbls.BLS12_381)
	hbls.SetETHmode(hbls.EthModeLatest)
}

// Config holds withdrawal request configuration.
type Config struct {
	Mnemonic                  string
	WithdrawalRequestContract ethcommon.Address
	WithdrawAmount            uint64 // gwei, 0 = full exit
	FeeLimit                  *big.Int
	StartIndex                uint64 // first deposited validator index
	MaxIndex                  uint64 // one past last deposited index
	CycleStartIndex           uint64 // where in the cycle to resume
	Count                     uint64
	BatchSize                 uint64
	StorageInterval           uint64
}

// Runner generates and submits withdrawal request transactions.
type Runner struct {
	log     logrus.FieldLogger
	cfg     Config
	tracker *tracker.Tracker
	seed    []byte
}

// NewRunner creates a withdrawal request runner.
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

	if err := e2types.InitBLS(); err != nil {
		// May already be initialized
	}

	if cfg.MaxIndex <= cfg.StartIndex {
		return nil, fmt.Errorf(
			"max-index (%d) must be greater than start-index (%d)",
			cfg.MaxIndex, cfg.StartIndex,
		)
	}

	return &Runner{
		log:     log.WithField("package", "withdrawals"),
		cfg:     cfg,
		tracker: trk,
		seed:    seed,
	}, nil
}

// Run generates and submits withdrawal requests in multicall batches.
// Returns the number of requests successfully submitted.
func (r *Runner) Run(ctx context.Context) (uint64, error) {
	indexRange := r.cfg.MaxIndex - r.cfg.StartIndex
	r.log.Infof(
		"starting withdrawal requests: count=%d amount=%d gwei range=[%d,%d) cycleStart=%d feeLimit=%s wei",
		r.cfg.Count, r.cfg.WithdrawAmount,
		r.cfg.StartIndex, r.cfg.MaxIndex, r.cfg.CycleStartIndex,
		r.cfg.FeeLimit.String(),
	)

	state, err := r.tracker.GetValues(ctx, tracker.WithdrawalKeys())
	if err != nil {
		return 0, fmt.Errorf("failed to read tracker state: %w", err)
	}

	dailyTimestamp := state[tracker.KeyDailyWithdrawalTimestamp]
	if dailyTimestamp == 0 {
		dailyTimestamp = uint64(time.Now().Unix())
	}

	var submitted uint64
	var feeCalldatas [][]byte

	for i := uint64(0); i < r.cfg.Count; i++ {
		select {
		case <-ctx.Done():
			return submitted, ctx.Err()
		default:
		}

		// Cyclic index: wrap around the deposited range
		cyclePos := (r.cfg.CycleStartIndex + i) % indexRange
		accountIdx := r.cfg.StartIndex + cyclePos

		calldata, err := r.generateWithdrawalCalldata(accountIdx)
		if err != nil {
			return submitted, fmt.Errorf(
				"failed to generate withdrawal request for index %d: %w",
				accountIdx, err,
			)
		}

		feeCalldatas = append(feeCalldatas, calldata)
		r.log.Infof(
			"prepared withdrawal request %d/%d (index: %d)",
			i+1, r.cfg.Count, accountIdx,
		)

		// Send batch via multicallFee
		if uint64(len(feeCalldatas)) >= r.cfg.BatchSize || i == r.cfg.Count-1 {
			batchLen := uint64(len(feeCalldatas))
			newSubmitted := submitted + batchLen

			// Build extra calls (storage update)
			var extraCalls []tracker.Call
			needsStorageUpdate := newSubmitted%r.cfg.StorageInterval == 0 || i == r.cfg.Count-1
			if needsStorageUpdate {
				newCycleIdx := (r.cfg.CycleStartIndex + newSubmitted) % indexRange
				stateCall, err := r.tracker.SetValuesCall(map[string]uint64{
					tracker.KeyTotalWithdrawalRequests:  state[tracker.KeyTotalWithdrawalRequests] + newSubmitted,
					tracker.KeyLastWithdrawalCycleIndex: newCycleIdx,
					tracker.KeyDailyWithdrawalCount:     state[tracker.KeyDailyWithdrawalCount] + newSubmitted,
					tracker.KeyDailyWithdrawalTimestamp: dailyTimestamp,
				})
				if err != nil {
					r.log.Warnf("failed to build state update call: %v", err)
				} else {
					extraCalls = append(extraCalls, *stateCall)
				}
			}

			r.log.Infof("submitting multicallFee batch of %d withdrawal requests...", batchLen)
			err := r.tracker.MulticallFee(
				ctx,
				r.cfg.WithdrawalRequestContract,
				feeCalldatas,
				r.cfg.FeeLimit,
				extraCalls,
				200000,
			)
			if err != nil {
				return submitted, fmt.Errorf(
					"multicallFee batch failed: %w", err,
				)
			}
			submitted = newSubmitted
			feeCalldatas = feeCalldatas[:0]
		}
	}

	return submitted, nil
}

// generateWithdrawalCalldata builds the 56-byte calldata for a
// withdrawal request: pubkey (48) + amount (8 big-endian).
func (r *Runner) generateWithdrawalCalldata(
	accountIdx uint64,
) ([]byte, error) {
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
	pubkey := validatorPrivkey.PublicKey().Marshal()

	txData := make([]byte, 56)
	copy(txData[0:48], pubkey)
	amount := new(big.Int).SetUint64(r.cfg.WithdrawAmount)
	amountBytes := amount.FillBytes(make([]byte, 8))
	copy(txData[48:], amountBytes)

	return txData, nil
}

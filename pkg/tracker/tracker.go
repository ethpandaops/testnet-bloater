package tracker

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethpandaops/spamoor/spamoor"
	"github.com/ethpandaops/spamoor/txbuilder"
	"github.com/holiman/uint256"
	"github.com/sirupsen/logrus"
)

// Tracker manages on-chain key-value storage via EIP-7702 delegation.
type Tracker struct {
	log        logrus.FieldLogger
	clientPool *spamoor.ClientPool
	txpool     *spamoor.TxPool
	wallet     *spamoor.Wallet
	rootWallet *spamoor.RootWallet
	eoaAddr    common.Address
	parsedABI  abi.ABI
	seed       string
}

// NewTracker creates a tracker bound to an RPC client and wallet.
func NewTracker(
	log logrus.FieldLogger,
	rpcURL string,
	privkey string,
	seed string,
) (*Tracker, error) {
	ctx := context.Background()
	clientPool := spamoor.NewClientPool(ctx, log)

	err := clientPool.InitClients([]*spamoor.ClientOptions{
		{RpcHost: rpcURL},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to init clients: %w", err)
	}

	err = clientPool.PrepareClients()
	if err != nil {
		return nil, fmt.Errorf("failed to prepare clients: %w", err)
	}

	txpool := spamoor.NewTxPool(&spamoor.TxPoolOptions{
		Context:    ctx,
		ClientPool: clientPool,
		ChainId:    clientPool.GetChainId(),
	})

	rootWallet, err := spamoor.InitRootWallet(
		ctx, privkey, clientPool, txpool, log,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to init wallet: %w", err)
	}

	parsedABI, err := abi.JSON(strings.NewReader(trackerABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse tracker ABI: %w", err)
	}

	wallet := rootWallet.GetWallet()

	return &Tracker{
		log:        log.WithField("package", "tracker"),
		clientPool: clientPool,
		txpool:     txpool,
		wallet:     wallet,
		rootWallet: rootWallet,
		eoaAddr:    wallet.GetAddress(),
		parsedABI:  parsedABI,
		seed:       seed,
	}, nil
}

// StorageKey computes keccak256(seed + "." + name) for use as a
// storage key in the on-chain mapping.
func (t *Tracker) StorageKey(name string) [32]byte {
	input := t.seed + "." + name
	return crypto.Keccak256Hash([]byte(input))
}

// GetWallet returns the underlying wallet.
func (t *Tracker) GetWallet() *spamoor.Wallet {
	return t.wallet
}

// GetTxPool returns the tx pool.
func (t *Tracker) GetTxPool() *spamoor.TxPool {
	return t.txpool
}

// GetClientPool returns the client pool.
func (t *Tracker) GetClientPool() *spamoor.ClientPool {
	return t.clientPool
}

// GetClient returns a client from the pool.
func (t *Tracker) GetClient() *spamoor.Client {
	return t.clientPool.GetClient()
}

// HasDelegation checks if the wallet EOA has an EIP-7702
// delegation set (code starts with 0xef0100).
func (t *Tracker) HasDelegation(ctx context.Context) (bool, error) {
	client := t.clientPool.GetClient()
	ethClient := client.GetEthClient()

	code, err := ethClient.CodeAt(ctx, t.eoaAddr, nil)
	if err != nil {
		return false, fmt.Errorf("failed to get code: %w", err)
	}

	// EIP-7702 delegation indicator: 0xef0100 + 20-byte address
	return len(code) == 23 &&
		code[0] == 0xef &&
		code[1] == 0x01 &&
		code[2] == 0x00, nil
}

// Deploy deploys the storage contract and sets EIP-7702
// delegation on the wallet EOA.
func (t *Tracker) Deploy(
	ctx context.Context,
) (common.Address, error) {
	bytecode, err := hex.DecodeString(
		strings.TrimPrefix(trackerBytecode, "0x"),
	)
	if err != nil {
		return common.Address{},
			fmt.Errorf("failed to decode bytecode: %w", err)
	}

	client := t.clientPool.GetClient()

	suggestedFee, suggestedTip, err := client.GetSuggestedFee(ctx)
	if err != nil {
		return common.Address{},
			fmt.Errorf("failed to get suggested fee: %w", err)
	}

	dynFeeTx, err := txbuilder.DynFeeTx(&txbuilder.TxMetadata{
		GasFeeCap: uint256.MustFromBig(suggestedFee),
		GasTipCap: uint256.MustFromBig(suggestedTip),
		Gas:       800000,
		Value:     uint256.NewInt(0),
		Data:      bytecode,
	})
	if err != nil {
		return common.Address{},
			fmt.Errorf("failed to build deploy tx: %w", err)
	}

	tx, err := t.wallet.BuildDynamicFeeTx(dynFeeTx)
	if err != nil {
		return common.Address{},
			fmt.Errorf("failed to sign deploy tx: %w", err)
	}

	t.log.Infof(
		"deploying storage contract (tx: %s)", tx.Hash().Hex(),
	)

	receipt, err := t.txpool.SendAndAwaitTransaction(
		ctx, t.wallet, tx, &spamoor.SendTransactionOptions{
			Client:      client,
			Rebroadcast: true,
		},
	)
	if err != nil {
		return common.Address{},
			fmt.Errorf("deploy tx failed: %w", err)
	}

	if receipt.Status != 1 {
		return common.Address{}, fmt.Errorf("deploy tx reverted")
	}

	contractAddr := receipt.ContractAddress
	t.log.Infof(
		"storage contract deployed at %s", contractAddr.Hex(),
	)

	// Set EIP-7702 delegation.
	chainID := t.clientPool.GetChainId()

	// Auth nonce must be tx_nonce + 1 when sender == authority,
	// because EIP-7702 increments sender nonce before processing
	// authorization tuples.
	txNonce := t.wallet.GetNonce()

	auth := ethtypes.SetCodeAuthorization{
		ChainID: *uint256.MustFromBig(chainID),
		Address: contractAddr,
		Nonce:   txNonce + 1,
	}

	signedAuth, err := ethtypes.SignSetCode(
		t.wallet.GetPrivateKey(), auth,
	)
	if err != nil {
		return common.Address{},
			fmt.Errorf("failed to sign authorization: %w", err)
	}

	suggestedFee, suggestedTip, err = client.GetSuggestedFee(ctx)
	if err != nil {
		return common.Address{},
			fmt.Errorf("failed to get suggested fee: %w", err)
	}

	setCodeTx, err := txbuilder.SetCodeTx(&txbuilder.TxMetadata{
		GasFeeCap: uint256.MustFromBig(suggestedFee),
		GasTipCap: uint256.MustFromBig(suggestedTip),
		Gas:       100000,
		To:        &t.eoaAddr,
		Value:     uint256.NewInt(0),
		AuthList: []ethtypes.SetCodeAuthorization{
			signedAuth,
		},
	})
	if err != nil {
		return common.Address{},
			fmt.Errorf("failed to build setcode tx: %w", err)
	}

	tx2, err := t.wallet.BuildSetCodeTx(setCodeTx)
	if err != nil {
		return common.Address{},
			fmt.Errorf("failed to sign setcode tx: %w", err)
	}

	t.log.Infof(
		"setting EIP-7702 delegation (tx: %s)", tx2.Hash().Hex(),
	)

	receipt2, err := t.txpool.SendAndAwaitTransaction(
		ctx, t.wallet, tx2, &spamoor.SendTransactionOptions{
			Client:      client,
			Rebroadcast: true,
		},
	)
	if err != nil {
		return common.Address{},
			fmt.Errorf("setcode tx failed: %w", err)
	}

	if receipt2.Status != 1 {
		return common.Address{},
			fmt.Errorf("setcode tx reverted")
	}

	t.log.Infof(
		"EIP-7702 delegation set to %s", contractAddr.Hex(),
	)

	return contractAddr, nil
}

// GetValues reads multiple named values from on-chain storage.
func (t *Tracker) GetValues(
	ctx context.Context,
	names []string,
) (map[string]uint64, error) {
	client := t.clientPool.GetClient()
	ethClient := client.GetEthClient()

	keys := make([][32]byte, len(names))
	for i, name := range names {
		keys[i] = t.StorageKey(name)
	}

	data, err := t.parsedABI.Pack("getMultiple", keys)
	if err != nil {
		return nil, fmt.Errorf("failed to pack getMultiple: %w", err)
	}

	result, err := ethClient.CallContract(
		ctx, ethereum.CallMsg{
			To:   &t.eoaAddr,
			Data: data,
		}, nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to call getMultiple: %w", err)
	}

	if len(result) < 32 {
		// No delegation set yet, return zeros
		values := make(map[string]uint64, len(names))
		for _, name := range names {
			values[name] = 0
		}
		return values, nil
	}

	outputs, err := t.parsedABI.Unpack("getMultiple", result)
	if err != nil {
		return nil, fmt.Errorf(
			"failed to unpack getMultiple: %w", err,
		)
	}

	bigValues, ok := outputs[0].([]*big.Int)
	if !ok {
		return nil, fmt.Errorf("unexpected return type from getMultiple")
	}

	values := make(map[string]uint64, len(names))
	for i, name := range names {
		if i < len(bigValues) {
			values[name] = bigValues[i].Uint64()
		}
	}

	return values, nil
}

// SetValues writes multiple named values to on-chain storage via
// a self-transfer.
func (t *Tracker) SetValues(
	ctx context.Context,
	values map[string]uint64,
) error {
	keys := make([][32]byte, 0, len(values))
	vals := make([]*big.Int, 0, len(values))

	for name, val := range values {
		keys = append(keys, t.StorageKey(name))
		vals = append(vals, new(big.Int).SetUint64(val))
	}

	data, err := t.parsedABI.Pack("setMultiple", keys, vals)
	if err != nil {
		return fmt.Errorf("failed to pack setMultiple: %w", err)
	}

	return t.sendSelfTx(ctx, data)
}

// SetValuesCall builds a Call that updates named values via the
// delegate's setMultiple function. Can be appended to a multicall batch.
func (t *Tracker) SetValuesCall(
	values map[string]uint64,
) (*Call, error) {
	keys := make([][32]byte, 0, len(values))
	vals := make([]*big.Int, 0, len(values))

	for name, val := range values {
		keys = append(keys, t.StorageKey(name))
		vals = append(vals, new(big.Int).SetUint64(val))
	}

	data, err := t.parsedABI.Pack("setMultiple", keys, vals)
	if err != nil {
		return nil, fmt.Errorf("failed to pack setMultiple: %w", err)
	}

	return &Call{
		Target:   t.eoaAddr,
		Calldata: data,
		Value:    new(big.Int),
	}, nil
}

// Call represents a single call in a multicall batch.
type Call struct {
	Target   common.Address
	Calldata []byte
	Value    *big.Int
}

// Multicall executes multiple calls in a single self-transfer tx
// via the delegate's multicall function.
func (t *Tracker) Multicall(
	ctx context.Context,
	calls []Call,
	gasPerCall uint64,
) error {
	targets := make([]common.Address, len(calls))
	calldatas := make([][]byte, len(calls))
	values := make([]*big.Int, len(calls))

	totalValue := new(big.Int)
	for i, c := range calls {
		targets[i] = c.Target
		calldatas[i] = c.Calldata
		values[i] = c.Value
		totalValue.Add(totalValue, c.Value)
	}

	data, err := t.parsedABI.Pack("multicall", targets, calldatas, values)
	if err != nil {
		return fmt.Errorf("failed to pack multicall: %w", err)
	}

	client := t.clientPool.GetClient()

	suggestedFee, suggestedTip, err := client.GetSuggestedFee(ctx)
	if err != nil {
		return fmt.Errorf("failed to get suggested fee: %w", err)
	}

	gas := 50000 + gasPerCall*uint64(len(calls))

	dynFeeTx, err := txbuilder.DynFeeTx(&txbuilder.TxMetadata{
		GasFeeCap: uint256.MustFromBig(suggestedFee),
		GasTipCap: uint256.MustFromBig(suggestedTip),
		Gas:       gas,
		To:        &t.eoaAddr,
		Value:     uint256.NewInt(0),
		Data:      data,
	})
	if err != nil {
		return fmt.Errorf("failed to build multicall tx: %w", err)
	}

	tx, err := t.wallet.BuildDynamicFeeTx(dynFeeTx)
	if err != nil {
		return fmt.Errorf("failed to sign multicall tx: %w", err)
	}

	t.log.Infof(
		"sending multicall with %d calls (tx: %s)",
		len(calls), tx.Hash().Hex(),
	)

	receipt, err := t.txpool.SendAndAwaitTransaction(
		ctx, t.wallet, tx, &spamoor.SendTransactionOptions{
			Client:      client,
			Rebroadcast: true,
		},
	)
	if err != nil {
		return fmt.Errorf("multicall tx failed: %w", err)
	}

	if receipt.Status != 1 {
		return fmt.Errorf("multicall tx reverted")
	}

	return nil
}

// MulticallFee executes fee-based calls where the fee is read on-chain.
// feeCalldatas are sent to feeContract with the on-chain fee as value.
// extraCalls are additional calls (e.g. storage updates).
// feeLimit is the maximum acceptable fee; the tx reverts if exceeded.
func (t *Tracker) MulticallFee(
	ctx context.Context,
	feeContract common.Address,
	feeCalldatas [][]byte,
	feeLimit *big.Int,
	extraCalls []Call,
	gasPerCall uint64,
) error {
	extraTargets := make([]common.Address, len(extraCalls))
	extraCalldatasArr := make([][]byte, len(extraCalls))
	extraValues := make([]*big.Int, len(extraCalls))

	for i, c := range extraCalls {
		extraTargets[i] = c.Target
		extraCalldatasArr[i] = c.Calldata
		extraValues[i] = c.Value
	}

	data, err := t.parsedABI.Pack(
		"multicallFee",
		feeContract,
		feeCalldatas,
		feeLimit,
		extraTargets,
		extraCalldatasArr,
		extraValues,
	)
	if err != nil {
		return fmt.Errorf("failed to pack multicallFee: %w", err)
	}

	client := t.clientPool.GetClient()

	suggestedFee, suggestedTip, err := client.GetSuggestedFee(ctx)
	if err != nil {
		return fmt.Errorf("failed to get suggested fee: %w", err)
	}

	gas := 50000 + gasPerCall*uint64(len(feeCalldatas)+len(extraCalls))

	dynFeeTx, err := txbuilder.DynFeeTx(&txbuilder.TxMetadata{
		GasFeeCap: uint256.MustFromBig(suggestedFee),
		GasTipCap: uint256.MustFromBig(suggestedTip),
		Gas:       gas,
		To:        &t.eoaAddr,
		Value:     uint256.NewInt(0),
		Data:      data,
	})
	if err != nil {
		return fmt.Errorf("failed to build multicallFee tx: %w", err)
	}

	tx, err := t.wallet.BuildDynamicFeeTx(dynFeeTx)
	if err != nil {
		return fmt.Errorf("failed to sign multicallFee tx: %w", err)
	}

	t.log.Infof(
		"sending multicallFee with %d fee calls + %d extra calls (tx: %s)",
		len(feeCalldatas), len(extraCalls), tx.Hash().Hex(),
	)

	receipt, err := t.txpool.SendAndAwaitTransaction(
		ctx, t.wallet, tx, &spamoor.SendTransactionOptions{
			Client:      client,
			Rebroadcast: true,
		},
	)
	if err != nil {
		return fmt.Errorf("multicallFee tx failed: %w", err)
	}

	if receipt.Status != 1 {
		return fmt.Errorf("multicallFee tx reverted")
	}

	return nil
}

// sendSelfTx sends a self-transfer with the given calldata to
// update state.
func (t *Tracker) sendSelfTx(
	ctx context.Context,
	data []byte,
) error {
	client := t.clientPool.GetClient()

	suggestedFee, suggestedTip, err := client.GetSuggestedFee(ctx)
	if err != nil {
		return fmt.Errorf("failed to get suggested fee: %w", err)
	}

	dynFeeTx, err := txbuilder.DynFeeTx(&txbuilder.TxMetadata{
		GasFeeCap: uint256.MustFromBig(suggestedFee),
		GasTipCap: uint256.MustFromBig(suggestedTip),
		Gas:       200000,
		To:        &t.eoaAddr,
		Value:     uint256.NewInt(0),
		Data:      data,
	})
	if err != nil {
		return fmt.Errorf("failed to build self-tx: %w", err)
	}

	tx, err := t.wallet.BuildDynamicFeeTx(dynFeeTx)
	if err != nil {
		return fmt.Errorf("failed to sign self-tx: %w", err)
	}

	t.log.Infof(
		"updating on-chain state (tx: %s)", tx.Hash().Hex(),
	)

	receipt, err := t.txpool.SendAndAwaitTransaction(
		ctx, t.wallet, tx, &spamoor.SendTransactionOptions{
			Client:      client,
			Rebroadcast: true,
		},
	)
	if err != nil {
		return fmt.Errorf("state update tx failed: %w", err)
	}

	if receipt.Status != 1 {
		return fmt.Errorf("state update tx reverted")
	}

	return nil
}

// Close cleans up resources.
func (t *Tracker) Close() {
	t.rootWallet.Shutdown()
}

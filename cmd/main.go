package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"os/signal"
	"syscall"

	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/testnet-bloater/pkg/deposits"
	"github.com/ethpandaops/testnet-bloater/pkg/tracker"
	"github.com/ethpandaops/testnet-bloater/pkg/withdrawals"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

func main() {
	log := logrus.New()

	rootCmd := &cobra.Command{
		Use:   "bloater-tool",
		Short: "Testnet bloater operations tool",
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			level, _ := cmd.Flags().GetString("log-level")
			lvl, err := logrus.ParseLevel(level)
			if err != nil {
				lvl = logrus.InfoLevel
			}
			log.SetLevel(lvl)
		},
	}

	rootCmd.PersistentFlags().String("rpc-url", "", "Execution layer RPC URL")
	rootCmd.PersistentFlags().String("private-key", "", "Wallet private key (hex)")
	rootCmd.PersistentFlags().String("storage-seed", "v1", "Storage key seed (change to reset counters)")
	rootCmd.PersistentFlags().String("log-level", "info", "Log level")

	rootCmd.AddCommand(
		newDeployTrackerCmd(log),
		newEnsureTrackerCmd(log),
		newReadStateCmd(log),
		newRunDepositsCmd(log),
		newRunWithdrawalsCmd(log),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func newContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		cancel()
	}()
	return ctx
}

func initTracker(
	cmd *cobra.Command,
	log logrus.FieldLogger,
) (*tracker.Tracker, error) {
	rpcURL, _ := cmd.Flags().GetString("rpc-url")
	privkey, _ := cmd.Flags().GetString("private-key")
	seed, _ := cmd.Flags().GetString("storage-seed")
	if rpcURL == "" {
		return nil, fmt.Errorf("--rpc-url is required")
	}
	if privkey == "" {
		return nil, fmt.Errorf("--private-key is required")
	}
	return tracker.NewTracker(log, rpcURL, privkey, seed)
}

// deploy-tracker subcommand
func newDeployTrackerCmd(log *logrus.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "deploy-tracker",
		Short: "Deploy tracker contract and set EIP-7702 delegation",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := newContext()
			trk, err := initTracker(cmd, log)
			if err != nil {
				return err
			}
			defer trk.Close()

			addr, err := trk.Deploy(ctx)
			if err != nil {
				return fmt.Errorf("deploy failed: %w", err)
			}

			fmt.Printf(
				`{"contract_address":"%s"}`+"\n",
				addr.Hex(),
			)
			return nil
		},
	}
}

// ensure-tracker subcommand
func newEnsureTrackerCmd(log *logrus.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "ensure-tracker",
		Short: "Deploy tracker and set delegation if not already set",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := newContext()
			trk, err := initTracker(cmd, log)
			if err != nil {
				return err
			}
			defer trk.Close()

			has, err := trk.HasDelegation(ctx)
			if err != nil {
				return fmt.Errorf("delegation check failed: %w", err)
			}

			if has {
				fmt.Println(`{"status":"already_set"}`)
				return nil
			}

			addr, err := trk.Deploy(ctx)
			if err != nil {
				return fmt.Errorf("deploy failed: %w", err)
			}

			fmt.Printf(
				`{"status":"deployed","contract_address":"%s"}`+"\n",
				addr.Hex(),
			)
			return nil
		},
	}
}

// read-state subcommand
func newReadStateCmd(log *logrus.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "read-state",
		Short: "Read on-chain tracker state",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := newContext()
			trk, err := initTracker(cmd, log)
			if err != nil {
				return err
			}
			defer trk.Close()

			values, err := trk.GetValues(ctx, tracker.AllKeys())
			if err != nil {
				return fmt.Errorf("read state failed: %w", err)
			}

			out, err := json.Marshal(values)
			if err != nil {
				return fmt.Errorf("json marshal failed: %w", err)
			}

			fmt.Println(string(out))
			return nil
		},
	}
}

// run-deposits subcommand
func newRunDepositsCmd(log *logrus.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run-deposits",
		Short: "Generate and submit deposit transactions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := newContext()
			trk, err := initTracker(cmd, log)
			if err != nil {
				return err
			}
			defer trk.Close()

			mnemonic, _ := cmd.Flags().GetString("mnemonic")
			depositContract, _ := cmd.Flags().GetString("deposit-contract")
			depositAmount, _ := cmd.Flags().GetUint64("deposit-amount")
			startIndex, _ := cmd.Flags().GetUint64("start-index")
			count, _ := cmd.Flags().GetUint64("count")
			batchSize, _ := cmd.Flags().GetUint64("batch-size")
			storageInterval, _ := cmd.Flags().GetUint64("storage-interval")
			forkVersion, _ := cmd.Flags().GetString("genesis-fork-version")

			runner, err := deposits.NewRunner(log, deposits.Config{
				Mnemonic:           mnemonic,
				DepositContract:    ethcommon.HexToAddress(depositContract),
				DepositAmount:      depositAmount,
				StartIndex:         startIndex,
				Count:              count,
				BatchSize:          batchSize,
				StorageInterval:    storageInterval,
				GenesisForkVersion: forkVersion,
			}, trk)
			if err != nil {
				return fmt.Errorf("failed to create deposit runner: %w", err)
			}

			submitted, err := runner.Run(ctx)
			if err != nil {
				return fmt.Errorf("deposit run failed: %w", err)
			}

			fmt.Printf(`{"submitted":%d}`+"\n", submitted)
			return nil
		},
	}

	cmd.Flags().String("mnemonic", "", "Validator mnemonic")
	cmd.Flags().String("deposit-contract", "", "Deposit contract address")
	cmd.Flags().Uint64("deposit-amount", 32000000000, "Deposit amount in gwei")
	cmd.Flags().Uint64("start-index", 0, "Starting validator index")
	cmd.Flags().Uint64("count", 1, "Number of deposits")
	cmd.Flags().Uint64("batch-size", 16, "Txs per batch")
	cmd.Flags().Uint64("storage-interval", 50, "Update storage every N txs")
	cmd.Flags().String("genesis-fork-version", "0x00000000", "Genesis fork version hex")

	return cmd
}

// run-withdrawals subcommand
func newRunWithdrawalsCmd(log *logrus.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run-withdrawals",
		Short: "Generate and submit withdrawal request transactions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := newContext()
			trk, err := initTracker(cmd, log)
			if err != nil {
				return err
			}
			defer trk.Close()

			mnemonic, _ := cmd.Flags().GetString("mnemonic")
			contract, _ := cmd.Flags().GetString("withdrawal-request-contract")
			amount, _ := cmd.Flags().GetUint64("withdraw-amount")
			feeLimitStr, _ := cmd.Flags().GetString("fee-limit")
			startIndex, _ := cmd.Flags().GetUint64("start-index")
			maxIndex, _ := cmd.Flags().GetUint64("max-index")
			cycleStart, _ := cmd.Flags().GetUint64("cycle-start-index")
			count, _ := cmd.Flags().GetUint64("count")
			batchSize, _ := cmd.Flags().GetUint64("batch-size")
			storageInterval, _ := cmd.Flags().GetUint64("storage-interval")

			feeLimit, ok := new(big.Int).SetString(feeLimitStr, 10)
			if !ok {
				return fmt.Errorf("invalid fee-limit: %s", feeLimitStr)
			}

			runner, err := withdrawals.NewRunner(log, withdrawals.Config{
				Mnemonic:                  mnemonic,
				WithdrawalRequestContract: ethcommon.HexToAddress(contract),
				WithdrawAmount:            amount,
				FeeLimit:                  feeLimit,
				StartIndex:                startIndex,
				MaxIndex:                  maxIndex,
				CycleStartIndex:           cycleStart,
				Count:                     count,
				BatchSize:                 batchSize,
				StorageInterval:           storageInterval,
			}, trk)
			if err != nil {
				return fmt.Errorf(
					"failed to create withdrawal runner: %w", err,
				)
			}

			submitted, err := runner.Run(ctx)
			if err != nil {
				return fmt.Errorf("withdrawal run failed: %w", err)
			}

			fmt.Printf(`{"submitted":%d}`+"\n", submitted)
			return nil
		},
	}

	cmd.Flags().String("mnemonic", "", "Validator mnemonic")
	cmd.Flags().String(
		"withdrawal-request-contract",
		"0x00000961Ef480Eb55e80D19ad83579A64c007002",
		"Withdrawal request system contract",
	)
	cmd.Flags().Uint64("withdraw-amount", 0, "Withdrawal amount in gwei (0=full exit)")
	cmd.Flags().String("fee-limit", "1000000000000", "Maximum acceptable withdrawal request fee in wei")
	cmd.Flags().Uint64("start-index", 0, "First deposited validator index")
	cmd.Flags().Uint64("max-index", 0, "One past last deposited validator index")
	cmd.Flags().Uint64("cycle-start-index", 0, "Resume position in cycle")
	cmd.Flags().Uint64("count", 1, "Number of withdrawal requests")
	cmd.Flags().Uint64("batch-size", 16, "Txs per batch")
	cmd.Flags().Uint64("storage-interval", 50, "Update storage every N txs")

	return cmd
}

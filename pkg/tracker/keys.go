package tracker

// Storage key names for deposit tracking.
const (
	KeyTotalDeposits         = "totalDeposits"
	KeyLastDepositIndex      = "lastDepositIndex"
	KeyDailyDepositCount     = "dailyDepositCount"
	KeyDailyDepositTimestamp = "dailyDepositTimestamp"
)

// Storage key names for withdrawal request tracking.
const (
	KeyTotalWithdrawalRequests  = "totalWithdrawalRequests"
	KeyLastWithdrawalCycleIndex = "lastWithdrawalCycleIndex"
	KeyDailyWithdrawalCount     = "dailyWithdrawalCount"
	KeyDailyWithdrawalTimestamp = "dailyWithdrawalTimestamp"
)

// AllKeys returns all known storage key names.
func AllKeys() []string {
	return []string{
		KeyTotalDeposits,
		KeyLastDepositIndex,
		KeyDailyDepositCount,
		KeyDailyDepositTimestamp,
		KeyTotalWithdrawalRequests,
		KeyLastWithdrawalCycleIndex,
		KeyDailyWithdrawalCount,
		KeyDailyWithdrawalTimestamp,
	}
}

// DepositKeys returns storage key names for deposit tracking.
func DepositKeys() []string {
	return []string{
		KeyTotalDeposits,
		KeyLastDepositIndex,
		KeyDailyDepositCount,
		KeyDailyDepositTimestamp,
	}
}

// WithdrawalKeys returns storage key names for withdrawal tracking.
func WithdrawalKeys() []string {
	return []string{
		KeyTotalWithdrawalRequests,
		KeyLastWithdrawalCycleIndex,
		KeyDailyWithdrawalCount,
		KeyDailyWithdrawalTimestamp,
	}
}

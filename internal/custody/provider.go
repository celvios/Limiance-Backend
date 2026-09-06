package custody

import "context"

// Provider is the only boundary that knows an MPC custodian's API.
// Fireblocks is the Phase 1 adapter; no ledger or HTTP handler may depend on its SDK.
type Provider interface {
	ProviderID() string
	CreateCustomerWallet(ctx context.Context, customerReference string) (Wallet, error)
	GetDepositAddress(ctx context.Context, walletID, assetID, idempotencyKey string) (DepositAddress, error)
	CreateWithdrawal(ctx context.Context, input WithdrawalRequest) (Withdrawal, error)
}

// WithdrawalObserver is an optional read-only capability used before dispatch
// and while reconciling an unknown outcome. It never mutates provider state.
type WithdrawalObserver interface {
	GetVaultAssetBalance(ctx context.Context, vaultID, assetID string) (VaultAssetBalance, error)
	EstimateWithdrawalFee(ctx context.Context, input WithdrawalRequest) (FeeEstimate, error)
	FindWithdrawalByExternalID(ctx context.Context, externalID string) (WithdrawalLookup, error)
}

type VaultAssetBalance struct {
	AssetID     string
	Available   string
	Locked      string
	BlockHeight string
	BlockHash   string
}

type FeeEstimate struct {
	NetworkFee string
}

type WithdrawalLookup struct {
	Found                 bool
	ProviderTransactionID string
	ExternalID            string
	Status                string
	TransactionHash       string
}

type Wallet struct {
	ID     string
	Status string
}
type DepositAddress struct {
	ID      string
	Address string
	Tag     string
}

type WithdrawalRequest struct {
	SourceVaultID  string
	AssetID        string
	Destination    string
	DestinationTag string
	Amount         string
	ExternalID     string
}

type Withdrawal struct {
	ProviderTransactionID string
	Status                string
}

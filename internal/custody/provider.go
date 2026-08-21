package custody

import "context"

// Provider is the only boundary that knows an MPC custodian's API.
// Fireblocks is the Phase 1 adapter; no ledger or HTTP handler may depend on its SDK.
type Provider interface {
	ProviderID() string
	CreateCustomerWallet(ctx context.Context, customerReference string) (Wallet, error)
	GetDepositAddress(ctx context.Context, walletID, assetID, idempotencyKey string) (DepositAddress, error)
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

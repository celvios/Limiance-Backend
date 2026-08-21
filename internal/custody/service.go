package custody

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/limiance/backend/internal/datamanager"
)

var (
	ErrInvalidDepositRequest = errors.New("invalid deposit address request")
	ErrRouteUnavailable      = errors.New("deposit route is unavailable")
	ErrProviderUnavailable   = errors.New("custody provider is unavailable")
)

type DepositRequest struct {
	AssetSymbol string `json:"asset_symbol"`
	Network     string `json:"network"`
}

type Service struct {
	data     *datamanager.Manager
	provider Provider
	policy   RoutePolicy
}

func NewService(data *datamanager.Manager, provider Provider, policies ...RoutePolicy) *Service {
	policy := RoutePolicy{}
	if len(policies) > 0 {
		policy = policies[0]
	}
	return &Service{data: data, provider: provider, policy: policy}
}

// DepositAddress returns the one active custody address for a user and an
// individually enabled asset/network route. It never credits a balance: chain
// observation, confirmations, risk review, and a ledger transaction are a
// separate Deposit Gateway workflow.
func (s *Service) DepositAddress(ctx context.Context, userID string, request DepositRequest) (datamanager.DepositAddress, error) {
	if s.provider == nil {
		return datamanager.DepositAddress{}, ErrProviderUnavailable
	}
	request.AssetSymbol = strings.ToUpper(strings.TrimSpace(request.AssetSymbol))
	request.Network = strings.ToLower(strings.TrimSpace(request.Network))
	if request.AssetSymbol == "" || request.Network == "" {
		return datamanager.DepositAddress{}, ErrInvalidDepositRequest
	}
	if err := s.policy.Validate(request.Network); err != nil {
		return datamanager.DepositAddress{}, err
	}
	asset, err := s.data.DepositAsset(ctx, request.AssetSymbol, request.Network)
	if errors.Is(err, pgx.ErrNoRows) {
		return datamanager.DepositAddress{}, ErrRouteUnavailable
	}
	if err != nil {
		return datamanager.DepositAddress{}, err
	}
	if existing, err := s.data.ActiveDepositAddress(ctx, userID, asset.ID, s.provider.ProviderID()); err == nil {
		return existing, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return datamanager.DepositAddress{}, err
	}

	wallet, err := s.data.CustodyWallet(ctx, userID, s.provider.ProviderID())
	if errors.Is(err, pgx.ErrNoRows) || wallet.ExternalVaultID == "" {
		external, providerErr := s.provider.CreateCustomerWallet(ctx, userID)
		if providerErr != nil {
			return datamanager.DepositAddress{}, providerErr
		}
		wallet, err = s.data.SaveCustodyWallet(ctx, userID, s.provider.ProviderID(), external.ID)
	}
	if err != nil {
		return datamanager.DepositAddress{}, err
	}
	providerAddress, err := s.provider.GetDepositAddress(ctx, wallet.ExternalVaultID, asset.CustodyAssetID, DeterministicIdempotencyKey("deposit-address", userID, asset.ID))
	if err != nil {
		return datamanager.DepositAddress{}, err
	}
	return s.data.SaveDepositAddress(ctx, userID, asset, wallet.ID, providerAddress.ID, providerAddress.Address, providerAddress.Tag)
}

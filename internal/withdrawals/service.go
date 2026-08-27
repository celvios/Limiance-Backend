package withdrawals

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/limiance/backend/internal/datamanager"
)

var ErrInvalidInput = errors.New("invalid withdrawal input")

type Input struct {
	SourceAccountID string `json:"source_account_id"`
	AssetSymbol     string `json:"asset_symbol"`
	Network         string `json:"network"`
	Address         string `json:"address"`
	Tag             string `json:"tag"`
	AmountAtomic    int64  `json:"amount_atomic"`
	IdempotencyKey  string
}
type Service struct{ data *datamanager.Manager }

func NewService(data *datamanager.Manager) *Service { return &Service{data: data} }

type AddressInput struct {
	AssetSymbol string `json:"asset_symbol"`
	Network     string `json:"network"`
	Address     string `json:"address"`
	Tag         string `json:"tag"`
	Label       string `json:"label"`
	Whitelisted bool   `json:"whitelisted"`
}

func (s *Service) ListAddresses(ctx context.Context, userID string) ([]datamanager.WithdrawalAddress, error) {
	return s.data.WithdrawalAddresses(ctx, userID)
}

func (s *Service) AddAddress(ctx context.Context, userID string, input AddressInput, cooldown time.Duration) (datamanager.WithdrawalAddress, error) {
	input.AssetSymbol, input.Network = strings.ToUpper(strings.TrimSpace(input.AssetSymbol)), strings.ToLower(strings.TrimSpace(input.Network))
	input.Address, input.Tag, input.Label = strings.TrimSpace(input.Address), strings.TrimSpace(input.Tag), strings.TrimSpace(input.Label)
	if userID == "" || input.AssetSymbol == "" || input.Network == "" || input.Address == "" || input.Label == "" || len(input.Label) > 100 || cooldown < 0 {
		return datamanager.WithdrawalAddress{}, ErrInvalidInput
	}
	return s.data.AddWithdrawalAddress(ctx, userID, input.AssetSymbol, input.Network, input.Address, input.Tag, input.Label, input.Whitelisted, cooldown)
}

func (s *Service) DisableAddress(ctx context.Context, userID, addressID string) (bool, error) {
	if userID == "" || strings.TrimSpace(addressID) == "" {
		return false, ErrInvalidInput
	}
	return s.data.DisableWithdrawalAddress(ctx, userID, addressID)
}

func (s *Service) History(ctx context.Context, userID string, limit int) ([]datamanager.WithdrawalHistoryItem, error) {
	return s.data.WithdrawalHistory(ctx, userID, limit)
}

func (s *Service) Cancel(ctx context.Context, userID, withdrawalID string) (datamanager.WithdrawalCancellationResult, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(withdrawalID) == "" {
		return datamanager.WithdrawalCancellationResult{}, ErrInvalidInput
	}
	return s.data.CancelWithdrawal(ctx, userID, strings.TrimSpace(withdrawalID))
}

func (s *Service) Request(ctx context.Context, userID string, input Input) (datamanager.WithdrawalResult, error) {
	input.AssetSymbol = strings.ToUpper(strings.TrimSpace(input.AssetSymbol))
	input.Network = strings.ToLower(strings.TrimSpace(input.Network))
	input.Address = strings.TrimSpace(input.Address)
	input.Tag = strings.TrimSpace(input.Tag)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.SourceAccountID == "" || input.AssetSymbol == "" || input.Network == "" || input.Address == "" || input.AmountAtomic <= 0 || len(input.IdempotencyKey) < 16 || len(input.IdempotencyKey) > 255 {
		return datamanager.WithdrawalResult{}, ErrInvalidInput
	}
	return s.data.RequestWithdrawal(ctx, datamanager.WithdrawalInput{UserID: userID, SourceAccountID: input.SourceAccountID, AssetSymbol: input.AssetSymbol, Network: input.Network, Address: input.Address, Tag: input.Tag, AmountAtomic: input.AmountAtomic, IdempotencyKey: input.IdempotencyKey})
}

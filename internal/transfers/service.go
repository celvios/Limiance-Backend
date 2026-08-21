package transfers

import (
	"context"
	"errors"
	"net/mail"
	"regexp"
	"strconv"
	"strings"

	"github.com/limiance/backend/internal/datamanager"
)

var ErrInvalidInput = errors.New("invalid transfer input")
var ErrRecipientNotFound = errors.New("recipient not found")

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

type Input struct {
	SourceAccountID      string `json:"source_account_id"`
	DestinationAccountID string `json:"destination_account_id"`
	AssetSymbol          string `json:"asset_symbol"`
	Network              string `json:"network"`
	AmountAtomic         int64  `json:"amount_atomic"`
	IdempotencyKey       string
}

type Result struct {
	TransferID string `json:"transfer_id"`
	JournalID  string `json:"journal_id"`
	Duplicate  bool   `json:"duplicate"`
}

type RecipientInput struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type Recipient struct {
	UID                  int64  `json:"uid"`
	DestinationAccountID string `json:"destination_account_id"`
}

type Service struct{ data *datamanager.Manager }

func NewService(data *datamanager.Manager) *Service { return &Service{data: data} }

func (s *Service) Create(ctx context.Context, userID string, input Input) (Result, error) {
	input.AssetSymbol = strings.ToUpper(strings.TrimSpace(input.AssetSymbol))
	input.Network = strings.TrimSpace(input.Network)
	if userID == "" || input.SourceAccountID == "" || input.DestinationAccountID == "" || input.SourceAccountID == input.DestinationAccountID || input.AssetSymbol == "" || input.Network == "" || input.AmountAtomic <= 0 || len(input.IdempotencyKey) < 16 || len(input.IdempotencyKey) > 255 {
		return Result{}, ErrInvalidInput
	}
	result, err := s.data.CreateInternalTransfer(ctx, datamanager.TransferInput{SenderUserID: userID, SourceAccountID: input.SourceAccountID, DestinationAccountID: input.DestinationAccountID, AssetSymbol: input.AssetSymbol, Network: input.Network, AmountAtomic: input.AmountAtomic, IdempotencyKey: input.IdempotencyKey})
	if err != nil {
		return Result{}, err
	}
	return Result{TransferID: result.TransferID, JournalID: result.JournalID, Duplicate: result.Duplicate}, nil
}

func (s *Service) ResolveRecipient(ctx context.Context, input RecipientInput) (Recipient, error) {
	kind, value := strings.ToLower(strings.TrimSpace(input.Type)), strings.TrimSpace(input.Value)
	switch kind {
	case "uid":
		if _, err := strconv.ParseInt(value, 10, 64); err != nil || value == "" {
			return Recipient{}, ErrInvalidInput
		}
	case "email":
		value = strings.ToLower(value)
		if _, err := mail.ParseAddress(value); err != nil {
			return Recipient{}, ErrInvalidInput
		}
	case "phone":
		if !e164.MatchString(value) {
			return Recipient{}, ErrInvalidInput
		}
	default:
		return Recipient{}, ErrInvalidInput
	}
	stored, err := s.data.ResolveTransferRecipient(ctx, kind, value)
	if err != nil {
		return Recipient{}, ErrRecipientNotFound
	}
	return Recipient{UID: stored.UID, DestinationAccountID: stored.AccountID}, nil
}

package p2p

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
)

var (
	atomicPattern = regexp.MustCompile(`^[0-9]+$`)
	fiatPattern   = regexp.MustCompile(`^[0-9]+(\.[0-9]{1,8})?$`)
	hexPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Service struct {
	store Store
	now   func() time.Time
	newID func() (string, error)
}

func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now, newID: randomUUID}
}

func (service *Service) Create(ctx context.Context, input CreateInput) (Trade, error) {
	input.UserID, input.AccountID = strings.TrimSpace(input.UserID), strings.TrimSpace(input.AccountID)
	input.AssetSymbol, input.AssetNetwork = strings.TrimSpace(input.AssetSymbol), strings.TrimSpace(input.AssetNetwork)
	input.AmountAtomic, input.FiatAmount = strings.TrimSpace(input.AmountAtomic), strings.TrimSpace(input.FiatAmount)
	input.FiatCurrency = strings.ToUpper(strings.TrimSpace(input.FiatCurrency))
	input.PaymentMethod, input.IdempotencyKey = strings.TrimSpace(input.PaymentMethod), strings.TrimSpace(input.IdempotencyKey)
	if err := validateIdempotency(input.IdempotencyKey); err != nil {
		return Trade{}, err
	}
	amount, amountOK := new(big.Int).SetString(input.AmountAtomic, 10)
	if input.UserID == "" || input.AccountID == "" || input.AssetSymbol == "" || input.AssetNetwork == "" ||
		!atomicPattern.MatchString(input.AmountAtomic) || !amountOK || amount.Sign() <= 0 ||
		len(input.FiatCurrency) != 3 || !fiatPattern.MatchString(input.FiatAmount) ||
		len(input.PaymentMethod) < 2 || len(input.PaymentMethod) > 100 || input.ExpiresInSeconds < 300 || input.ExpiresInSeconds > 7*24*60*60 {
		return Trade{}, ErrInvalidRequest
	}
	tradeID, err := service.newID()
	if err != nil {
		return Trade{}, err
	}
	now := service.now().UTC()
	hash := requestHash("create", input.UserID, input.AccountID, input.AssetSymbol, input.AssetNetwork, input.AmountAtomic, input.FiatCurrency, input.FiatAmount, input.PaymentMethod, fmt.Sprint(input.ExpiresInSeconds))
	return service.store.Create(ctx, CreateCommand{TradeID: tradeID, UserID: input.UserID, AccountID: input.AccountID,
		AssetSymbol: input.AssetSymbol, AssetNetwork: input.AssetNetwork, AmountAtomic: input.AmountAtomic,
		FiatCurrency: input.FiatCurrency, FiatAmount: input.FiatAmount, PaymentMethod: input.PaymentMethod,
		IdempotencyKey: input.IdempotencyKey, OfferExpiresAt: now.Add(time.Duration(input.ExpiresInSeconds) * time.Second), Now: now, RequestHash: hash})
}

func (service *Service) Accept(ctx context.Context, input ActionInput) (Trade, error) {
	command, err := service.action("accept", input)
	if err != nil {
		return Trade{}, err
	}
	command.PaymentDeadline = command.Now.Add(30 * time.Minute)
	return service.store.Accept(ctx, command)
}

func (service *Service) MarkPaid(ctx context.Context, input ActionInput) (Trade, error) {
	command, err := service.action("mark_paid", input)
	if err != nil {
		return Trade{}, err
	}
	return service.store.MarkPaid(ctx, command)
}

func (service *Service) Release(ctx context.Context, input ActionInput) (Trade, error) {
	command, err := service.action("release", input)
	if err != nil {
		return Trade{}, err
	}
	return service.store.Release(ctx, command)
}

func (service *Service) Cancel(ctx context.Context, input ActionInput) (Trade, error) {
	command, err := service.action("cancel", input)
	if err != nil {
		return Trade{}, err
	}
	return service.store.Cancel(ctx, command)
}

func (service *Service) Dispute(ctx context.Context, input DisputeInput) (Trade, error) {
	input.Reason = strings.TrimSpace(input.Reason)
	command, err := service.action("dispute", input.ActionInput)
	if err != nil {
		return Trade{}, err
	}
	if len(input.Reason) < 8 || len(input.Reason) > 1000 {
		return Trade{}, ErrInvalidRequest
	}
	command.RequestHash = requestHash("dispute", command.UserID, command.AccountID, command.TradeID, input.Reason)
	return service.store.Dispute(ctx, DisputeCommand{ActionCommand: command, Reason: input.Reason})
}

func (service *Service) AddEvidence(ctx context.Context, input EvidenceInput) (Trade, error) {
	input.EvidenceType, input.ObjectKey, input.SHA256Hex = strings.TrimSpace(input.EvidenceType), strings.TrimSpace(input.ObjectKey), strings.ToLower(strings.TrimSpace(input.SHA256Hex))
	command, err := service.action("evidence", input.ActionInput)
	if err != nil {
		return Trade{}, err
	}
	if len(input.EvidenceType) < 2 || len(input.EvidenceType) > 40 || len(input.ObjectKey) < 3 || len(input.ObjectKey) > 500 || !hexPattern.MatchString(input.SHA256Hex) {
		return Trade{}, ErrInvalidRequest
	}
	evidenceID, err := service.newID()
	if err != nil {
		return Trade{}, err
	}
	command.RequestHash = requestHash("evidence", command.UserID, command.TradeID, input.EvidenceType, input.ObjectKey, input.SHA256Hex)
	return service.store.AddEvidence(ctx, EvidenceCommand{ActionCommand: command, EvidenceID: evidenceID, EvidenceType: input.EvidenceType, ObjectKey: input.ObjectKey, SHA256Hex: input.SHA256Hex})
}

func (service *Service) Expire(ctx context.Context, limit int) ([]Trade, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	return service.store.Expire(ctx, service.now().UTC(), limit)
}

func (service *Service) ProposeResolution(ctx context.Context, input ResolutionInput) (Resolution, error) {
	input.ActorID, input.TradeID, input.Action, input.Reason, input.IdempotencyKey = strings.TrimSpace(input.ActorID), strings.TrimSpace(input.TradeID), strings.ToLower(strings.TrimSpace(input.Action)), strings.TrimSpace(input.Reason), strings.TrimSpace(input.IdempotencyKey)
	if err := validateIdempotency(input.IdempotencyKey); err != nil {
		return Resolution{}, err
	}
	if input.ActorID == "" || input.TradeID == "" || input.Action != "force_release" && input.Action != "refund" || len(input.Reason) < 8 || len(input.Reason) > 1000 {
		return Resolution{}, ErrInvalidRequest
	}
	id, err := service.newID()
	if err != nil {
		return Resolution{}, err
	}
	return service.store.ProposeResolution(ctx, ResolutionCommand{ResolutionID: id, ActorID: input.ActorID, TradeID: input.TradeID,
		Action: input.Action, Reason: input.Reason, IdempotencyKey: input.IdempotencyKey, Now: service.now().UTC(),
		RequestHash: requestHash("propose_resolution", input.ActorID, input.TradeID, input.Action, input.Reason)})
}

func (service *Service) ApproveResolution(ctx context.Context, input ApprovalInput) (Trade, error) {
	input.ActorID, input.ResolutionID, input.IdempotencyKey = strings.TrimSpace(input.ActorID), strings.TrimSpace(input.ResolutionID), strings.TrimSpace(input.IdempotencyKey)
	if err := validateIdempotency(input.IdempotencyKey); err != nil {
		return Trade{}, err
	}
	if input.ActorID == "" || input.ResolutionID == "" {
		return Trade{}, ErrInvalidRequest
	}
	return service.store.ApproveResolution(ctx, ApprovalCommand{ActorID: input.ActorID, ResolutionID: input.ResolutionID,
		IdempotencyKey: input.IdempotencyKey, Now: service.now().UTC(), RequestHash: requestHash("approve_resolution", input.ActorID, input.ResolutionID)})
}

func (service *Service) Get(ctx context.Context, userID, tradeID string) (Trade, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(tradeID) == "" {
		return Trade{}, ErrTradeNotFound
	}
	return service.store.Get(ctx, strings.TrimSpace(userID), strings.TrimSpace(tradeID))
}

func (service *Service) List(ctx context.Context, userID string, limit, offset int) ([]Trade, error) {
	if strings.TrimSpace(userID) == "" || offset < 0 {
		return nil, ErrInvalidRequest
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	return service.store.List(ctx, strings.TrimSpace(userID), limit, offset)
}

func (service *Service) ListOpen(ctx context.Context, limit, offset int) ([]Trade, error) {
	if offset < 0 {
		return nil, ErrInvalidRequest
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	return service.store.ListOpen(ctx, service.now().UTC(), limit, offset)
}

func (service *Service) ListDisputes(ctx context.Context, actorID string, limit, offset int) ([]Trade, error) {
	actorID = strings.TrimSpace(actorID)
	if actorID == "" || offset < 0 {
		return nil, ErrInvalidRequest
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	return service.store.ListDisputes(ctx, actorID, limit, offset)
}

func (service *Service) action(operation string, input ActionInput) (ActionCommand, error) {
	input.UserID, input.AccountID, input.TradeID, input.IdempotencyKey = strings.TrimSpace(input.UserID), strings.TrimSpace(input.AccountID), strings.TrimSpace(input.TradeID), strings.TrimSpace(input.IdempotencyKey)
	if err := validateIdempotency(input.IdempotencyKey); err != nil {
		return ActionCommand{}, err
	}
	if input.UserID == "" || input.AccountID == "" || input.TradeID == "" {
		return ActionCommand{}, ErrInvalidRequest
	}
	return ActionCommand{UserID: input.UserID, AccountID: input.AccountID, TradeID: input.TradeID,
		IdempotencyKey: input.IdempotencyKey, Now: service.now().UTC(), RequestHash: requestHash(operation, input.UserID, input.AccountID, input.TradeID)}, nil
}

func validateIdempotency(key string) error {
	if strings.TrimSpace(key) == "" {
		return ErrIdempotencyRequired
	}
	if len(key) > 255 {
		return ErrInvalidRequest
	}
	return nil
}

func requestHash(parts ...string) [32]byte { return sha256.Sum256([]byte(strings.Join(parts, "\x00"))) }

func randomUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

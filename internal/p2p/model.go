package p2p

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalidRequest       = errors.New("invalid p2p request")
	ErrIdempotencyRequired  = errors.New("p2p idempotency key is required")
	ErrIdempotencyConflict  = errors.New("p2p idempotency conflict")
	ErrFundingAccountNeeded = errors.New("active funding account is required")
	ErrInsufficientBalance  = errors.New("insufficient p2p escrow balance")
	ErrTradeNotFound        = errors.New("p2p trade not found")
	ErrInvalidTransition    = errors.New("invalid p2p state transition")
	ErrForbidden            = errors.New("p2p action is forbidden")
	ErrExpired              = errors.New("p2p trade deadline has expired")
	ErrApprovalRole         = errors.New("p2p approval role is required")
	ErrSameApprover         = errors.New("p2p maker and checker must be different users")
	ErrResolutionNotFound   = errors.New("p2p resolution request not found")
)

type Trade struct {
	ID              string     `json:"id"`
	SellerUserID    string     `json:"-"`
	SellerAccountID string     `json:"-"`
	BuyerUserID     string     `json:"-"`
	BuyerAccountID  string     `json:"-"`
	AssetID         string     `json:"-"`
	AssetSymbol     string     `json:"asset"`
	AssetNetwork    string     `json:"network"`
	AmountAtomic    string     `json:"amount_atomic"`
	FiatCurrency    string     `json:"fiat_currency"`
	FiatAmount      string     `json:"fiat_amount"`
	PaymentMethod   string     `json:"payment_method"`
	Status          string     `json:"status"`
	OfferExpiresAt  time.Time  `json:"offer_expires_at"`
	PaymentDeadline *time.Time `json:"payment_deadline,omitempty"`
	AcceptedAt      *time.Time `json:"accepted_at,omitempty"`
	PaidAt          *time.Time `json:"paid_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	Duplicate       bool       `json:"-"`
}

type Resolution struct {
	ID         string     `json:"id"`
	TradeID    string     `json:"trade_id"`
	Action     string     `json:"action"`
	Reason     string     `json:"reason"`
	ProposedBy string     `json:"proposed_by"`
	ApprovedBy string     `json:"approved_by,omitempty"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	DecidedAt  *time.Time `json:"decided_at,omitempty"`
	Duplicate  bool       `json:"-"`
}

type CreateInput struct {
	UserID           string
	AccountID        string
	AssetSymbol      string `json:"asset"`
	AssetNetwork     string `json:"network"`
	AmountAtomic     string `json:"amount_atomic"`
	FiatCurrency     string `json:"fiat_currency"`
	FiatAmount       string `json:"fiat_amount"`
	PaymentMethod    string `json:"payment_method"`
	ExpiresInSeconds int64  `json:"expires_in_seconds"`
	IdempotencyKey   string
}

type ActionInput struct {
	UserID         string
	AccountID      string
	TradeID        string
	IdempotencyKey string
}

type DisputeInput struct {
	ActionInput
	Reason string `json:"reason"`
}

type EvidenceInput struct {
	ActionInput
	EvidenceType string `json:"evidence_type"`
	ObjectKey    string `json:"object_key"`
	SHA256Hex    string `json:"sha256"`
}

type ResolutionInput struct {
	ActorID        string
	TradeID        string
	Action         string `json:"action"`
	Reason         string `json:"reason"`
	IdempotencyKey string
}

type ApprovalInput struct {
	ActorID        string
	ResolutionID   string
	IdempotencyKey string
}

type Store interface {
	Create(context.Context, CreateCommand) (Trade, error)
	Accept(context.Context, ActionCommand) (Trade, error)
	MarkPaid(context.Context, ActionCommand) (Trade, error)
	Release(context.Context, ActionCommand) (Trade, error)
	Cancel(context.Context, ActionCommand) (Trade, error)
	Dispute(context.Context, DisputeCommand) (Trade, error)
	AddEvidence(context.Context, EvidenceCommand) (Trade, error)
	Expire(context.Context, time.Time, int) ([]Trade, error)
	ProposeResolution(context.Context, ResolutionCommand) (Resolution, error)
	ApproveResolution(context.Context, ApprovalCommand) (Trade, error)
	Get(context.Context, string, string) (Trade, error)
	List(context.Context, string, int, int) ([]Trade, error)
	ListOpen(context.Context, time.Time, int, int) ([]Trade, error)
	ListDisputes(context.Context, string, int, int) ([]Trade, error)
}

type CreateCommand struct {
	TradeID, UserID, AccountID, AssetSymbol, AssetNetwork, AmountAtomic string
	FiatCurrency, FiatAmount, PaymentMethod, IdempotencyKey             string
	OfferExpiresAt, Now                                                 time.Time
	RequestHash                                                         [32]byte
}

type ActionCommand struct {
	UserID, AccountID, TradeID, IdempotencyKey string
	Now                                        time.Time
	PaymentDeadline                            time.Time
	RequestHash                                [32]byte
}

type DisputeCommand struct {
	ActionCommand
	Reason string
}

type EvidenceCommand struct {
	ActionCommand
	EvidenceID, EvidenceType, ObjectKey, SHA256Hex string
}

type ResolutionCommand struct {
	ResolutionID, ActorID, TradeID, Action, Reason, IdempotencyKey string
	Now                                                            time.Time
	RequestHash                                                    [32]byte
}

type ApprovalCommand struct {
	ActorID, ResolutionID, IdempotencyKey string
	Now                                   time.Time
	RequestHash                           [32]byte
}

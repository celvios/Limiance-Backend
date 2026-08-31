package p2p

import (
	"context"
	"errors"
	"testing"
	"time"
)

type storeStub struct {
	create  func(CreateCommand) (Trade, error)
	accept  func(ActionCommand) (Trade, error)
	dispute func(DisputeCommand) (Trade, error)
}

func (stub storeStub) Create(_ context.Context, command CreateCommand) (Trade, error) {
	return stub.create(command)
}
func (stub storeStub) Accept(_ context.Context, command ActionCommand) (Trade, error) {
	return stub.accept(command)
}
func (storeStub) MarkPaid(context.Context, ActionCommand) (Trade, error) { return Trade{}, nil }
func (storeStub) Release(context.Context, ActionCommand) (Trade, error)  { return Trade{}, nil }
func (storeStub) Cancel(context.Context, ActionCommand) (Trade, error)   { return Trade{}, nil }
func (stub storeStub) Dispute(_ context.Context, command DisputeCommand) (Trade, error) {
	return stub.dispute(command)
}
func (storeStub) AddEvidence(context.Context, EvidenceCommand) (Trade, error) { return Trade{}, nil }
func (storeStub) Expire(context.Context, time.Time, int) ([]Trade, error)     { return nil, nil }
func (storeStub) ProposeResolution(context.Context, ResolutionCommand) (Resolution, error) {
	return Resolution{}, nil
}
func (storeStub) ApproveResolution(context.Context, ApprovalCommand) (Trade, error) {
	return Trade{}, nil
}
func (storeStub) Get(context.Context, string, string) (Trade, error) { return Trade{}, nil }
func (storeStub) List(context.Context, string, int, int) ([]Trade, error) {
	return nil, nil
}
func (storeStub) ListOpen(context.Context, time.Time, int, int) ([]Trade, error) {
	return nil, nil
}
func (storeStub) ListDisputes(context.Context, string, int, int) ([]Trade, error) {
	return nil, nil
}

func TestCreateNormalizesAndHashesIntegerOnlyRequest(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	var captured CreateCommand
	service := NewService(storeStub{create: func(command CreateCommand) (Trade, error) {
		captured = command
		return Trade{ID: command.TradeID}, nil
	}})
	service.now = func() time.Time { return now }
	service.newID = func() (string, error) { return "00000000-0000-4000-8000-000000000001", nil }
	trade, err := service.Create(context.Background(), CreateInput{UserID: " seller ", AccountID: " funding ", AssetSymbol: "USDT", AssetNetwork: "tron", AmountAtomic: "1000000", FiatCurrency: "ngn", FiatAmount: "1500.25000000", PaymentMethod: "bank_transfer", ExpiresInSeconds: 600, IdempotencyKey: "create-1"})
	if err != nil || trade.ID == "" {
		t.Fatalf("create trade=%+v err=%v", trade, err)
	}
	if captured.UserID != "seller" || captured.FiatCurrency != "NGN" || captured.AmountAtomic != "1000000" || !captured.OfferExpiresAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("unexpected normalized command: %+v", captured)
	}
	if captured.RequestHash == ([32]byte{}) {
		t.Fatal("request hash was not populated")
	}
}

func TestCreateRejectsFloatingAtomicAndInvalidFiatPrecision(t *testing.T) {
	service := NewService(storeStub{create: func(CreateCommand) (Trade, error) { t.Fatal("store called"); return Trade{}, nil }})
	base := CreateInput{UserID: "seller", AccountID: "funding", AssetSymbol: "USDT", AssetNetwork: "tron", AmountAtomic: "100", FiatCurrency: "NGN", FiatAmount: "100.00", PaymentMethod: "bank", ExpiresInSeconds: 600, IdempotencyKey: "key"}
	for _, mutate := range []func(*CreateInput){
		func(input *CreateInput) { input.AmountAtomic = "1.5" },
		func(input *CreateInput) { input.FiatAmount = "1.000000001" },
	} {
		input := base
		mutate(&input)
		if _, err := service.Create(context.Background(), input); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid money accepted: %v", err)
		}
	}
}

func TestAcceptSetsServerControlledPaymentDeadline(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	service := NewService(storeStub{accept: func(command ActionCommand) (Trade, error) {
		if !command.PaymentDeadline.Equal(now.Add(30 * time.Minute)) {
			t.Fatalf("deadline=%s", command.PaymentDeadline)
		}
		return Trade{ID: command.TradeID}, nil
	}})
	service.now = func() time.Time { return now }
	if _, err := service.Accept(context.Background(), ActionInput{UserID: "buyer", AccountID: "funding", TradeID: "trade", IdempotencyKey: "accept-1"}); err != nil {
		t.Fatal(err)
	}
}

func TestDisputeStillRequiresIdempotency(t *testing.T) {
	service := NewService(storeStub{dispute: func(DisputeCommand) (Trade, error) { t.Fatal("store called"); return Trade{}, nil }})
	_, err := service.Dispute(context.Background(), DisputeInput{ActionInput: ActionInput{UserID: "buyer", AccountID: "funding", TradeID: "trade"}, Reason: "payment problem"})
	if !errors.Is(err, ErrIdempotencyRequired) {
		t.Fatalf("expected idempotency error, got %v", err)
	}
}

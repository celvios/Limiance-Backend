package phone

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/notifications"
)

var (
	ErrInvalidInput = errors.New("invalid phone verification input")
	ErrUnavailable  = errors.New("phone verification unavailable")
	ErrRejected     = errors.New("phone verification rejected")
	ErrPhoneInUse   = errors.New("phone already in use")
)

type Service struct {
	data     *datamanager.Manager
	provider *notifications.TwilioVerify
}

func NewService(data *datamanager.Manager, provider *notifications.TwilioVerify) *Service {
	return &Service{data: data, provider: provider}
}

func (s *Service) Start(ctx context.Context, userID, phone string) error {
	phone = strings.TrimSpace(phone)
	if userID == "" || phone == "" {
		return ErrInvalidInput
	}
	if s.provider == nil {
		return ErrUnavailable
	}
	result, err := s.provider.StartSMS(ctx, phone)
	if err != nil || result.Status == "" {
		return ErrRejected
	}
	return s.data.WithinTransaction(ctx, func(tx *datamanager.Transaction) error {
		return tx.Audit().Record(ctx, "user", "phone.verification_started", "user", userID, map[string]string{"provider": "twilio_verify", "verification_id": result.SID})
	})
}

func (s *Service) Verify(ctx context.Context, userID, phone, code string) error {
	phone, code = strings.TrimSpace(phone), strings.TrimSpace(code)
	if userID == "" || phone == "" || code == "" {
		return ErrInvalidInput
	}
	if s.provider == nil {
		return ErrUnavailable
	}
	result, err := s.provider.CheckSMS(ctx, phone, code)
	if err != nil || result.Status != "approved" {
		return ErrRejected
	}
	err = s.data.WithinTransaction(ctx, func(tx *datamanager.Transaction) error {
		if err := tx.Accounts().SetVerifiedPhone(ctx, userID, phone); err != nil {
			return err
		}
		return tx.Audit().Record(ctx, "user", "phone.verified", "user", userID, map[string]string{"provider": "twilio_verify", "verification_id": result.SID})
	})
	var dbError *pgconn.PgError
	if errors.As(err, &dbError) && dbError.Code == "23505" {
		return ErrPhoneInUse
	}
	return err
}

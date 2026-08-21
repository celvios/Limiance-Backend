package auth

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/security/envelope"
	"github.com/limiance/backend/internal/security/verification"
)

var ErrInvalidVerification = errors.New("invalid or expired verification code")

type VerifyEmailInput struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

type EmailVerificationService struct {
	data          *datamanager.Manager
	pepper        string
	encryptionKey string
}

func NewEmailVerificationService(data *datamanager.Manager, pepper, encryptionKey string) *EmailVerificationService {
	return &EmailVerificationService{data: data, pepper: pepper, encryptionKey: encryptionKey}
}

func (s *EmailVerificationService) Verify(ctx context.Context, input VerifyEmailInput) error {
	email := strings.ToLower(strings.TrimSpace(input.Email))
	if _, err := mail.ParseAddress(email); err != nil || len(input.Code) != 6 {
		return ErrInvalidVerification
	}
	hash, err := verification.Hash(s.pepper, input.Code)
	if err != nil {
		return err
	}
	return s.data.WithinTransaction(ctx, func(tx *datamanager.Transaction) error {
		userID, verified, err := tx.Verifications().VerifyEmailChallenge(ctx, email, hash)
		if err != nil {
			return err
		}
		if !verified {
			return ErrInvalidVerification
		}
		if err := tx.Audit().Record(ctx, "user", "user.email_verified", "user", userID, map[string]string{"email": email}); err != nil {
			return err
		}
		return tx.Outbox().Enqueue(ctx, "user.email_verified", "user", userID, map[string]string{"user_id": userID})
	})
}

// Resend always returns a nil error for unknown, active, or malformed email
// addresses. The HTTP response is intentionally indistinguishable so this
// unauthenticated endpoint cannot be used for account enumeration.
func (s *EmailVerificationService) Resend(ctx context.Context, emailInput string) error {
	email := strings.ToLower(strings.TrimSpace(emailInput))
	if _, err := mail.ParseAddress(email); err != nil {
		return nil
	}
	user, err := s.data.EmailVerificationUser(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && user.Status == "active") {
		return nil
	}
	if err != nil {
		return err
	}
	code, err := verification.NewCode()
	if err != nil {
		return err
	}
	codeHash, err := verification.Hash(s.pepper, code)
	if err != nil {
		return err
	}
	sealedCode, err := envelope.Seal(s.encryptionKey, code)
	if err != nil {
		return err
	}
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	return s.data.WithinTransaction(ctx, func(tx *datamanager.Transaction) error {
		if err := tx.Verifications().CreateEmailChallenge(ctx, user.ID, codeHash, expiresAt); err != nil {
			return err
		}
		if err := tx.Audit().Record(ctx, "user", "user.email_verification_resent", "user", user.ID, map[string]string{"email": user.Email}); err != nil {
			return err
		}
		return tx.Outbox().Enqueue(ctx, "email.verification_requested", "user", user.ID, map[string]any{"user_id": user.ID, "email": user.Email, "code_ciphertext": sealedCode, "expires_at": expiresAt.Format(time.RFC3339)})
	})
}

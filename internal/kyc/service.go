package kyc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/limiance/backend/internal/datamanager"
)

var ErrInvalidTransition = errors.New("invalid kyc status transition")
var ErrAccountNotActive = errors.New("account is not active")
var ErrAlreadyApproved = errors.New("kyc is already approved")

type SessionProvider interface {
	CreateApplicant(context.Context, string, string) (Applicant, error)
	CreateAccessToken(context.Context, string, string) (string, error)
}

type Status string

const (
	NotStarted Status = "not_started"
	Pending    Status = "pending"
	Approved   Status = "approved"
	Rejected   Status = "rejected"
	OnHold     Status = "on_hold"
	Expired    Status = "expired"
)

type Service struct{ data *datamanager.Manager }

func NewService(data *datamanager.Manager) *Service { return &Service{data: data} }

type Profile struct {
	Provider     string `json:"provider"`
	Status       Status `json:"status"`
	Tier         int16  `json:"tier"`
	LevelName    string `json:"level_name"`
	RejectReason string `json:"reject_reason,omitempty"`
	UpdatedAt    string `json:"updated_at"`
}

func (s *Service) Status(ctx context.Context, userID string) (Profile, error) {
	stored, err := s.data.KYCStatus(ctx, userID)
	if err != nil {
		return Profile{}, err
	}
	return Profile{
		Provider:     stored.Provider,
		Status:       Status(stored.Status),
		Tier:         stored.Tier,
		LevelName:    stored.LevelName,
		RejectReason: stored.RejectReason,
		UpdatedAt:    stored.UpdatedAt.UTC().Format(time.RFC3339),
	}, nil
}

type SessionService struct {
	data      *datamanager.Manager
	provider  SessionProvider
	levelName string
}

type Session struct {
	Token            string
	ExpiresInSeconds int
}

func NewSessionService(data *datamanager.Manager, provider SessionProvider, levelName string) *SessionService {
	return &SessionService{data: data, provider: provider, levelName: levelName}
}

func (s *SessionService) Create(ctx context.Context, userID string) (Session, error) {
	if s.provider == nil || s.levelName == "" {
		return Session{}, ErrNotConfigured
	}
	user, err := s.data.KYCSessionUser(ctx, userID)
	if err != nil {
		return Session{}, err
	}
	if user.Status != "active" {
		return Session{}, ErrAccountNotActive
	}
	if user.KYCStatus == string(Approved) {
		return Session{}, ErrAlreadyApproved
	}
	externalUserID := "limiance-" + user.ID
	applicantID := user.ApplicantID
	if applicantID == "" {
		applicant, err := s.provider.CreateApplicant(ctx, externalUserID, user.Email)
		if err != nil {
			return Session{}, err
		}
		applicantID = applicant.ID
	}
	token, err := s.provider.CreateAccessToken(ctx, externalUserID, user.Email)
	if err != nil {
		return Session{}, err
	}
	if err := s.data.WithinTransaction(ctx, func(tx *datamanager.Transaction) error {
		if err := tx.KYC().Start(ctx, user.ID, applicantID, s.levelName); err != nil {
			return err
		}
		if err := tx.Audit().Record(ctx, "user", "kyc.session_created", "user", user.ID, map[string]string{"provider": "sumsub", "applicant_id": applicantID, "level_name": s.levelName}); err != nil {
			return err
		}
		return tx.Outbox().Enqueue(ctx, "kyc.session_created", "user", user.ID, map[string]string{"user_id": user.ID, "provider": "sumsub", "applicant_id": applicantID})
	}); err != nil {
		return Session{}, fmt.Errorf("persist kyc session: %w", err)
	}
	return Session{Token: token, ExpiresInSeconds: 600}, nil
}

// Transition is invoked by a verified provider webhook or a privileged compliance action.
func (s *Service) Transition(ctx context.Context, userID string, from, to Status, tier int16, provider, applicantID string) error {
	if !allowed(from, to) {
		return ErrInvalidTransition
	}
	return s.data.WithinTransaction(ctx, func(tx *datamanager.Transaction) error {
		if err := tx.KYC().Transition(ctx, userID, string(from), string(to), tier, provider, applicantID); err != nil {
			return err
		}
		if err := tx.Audit().Record(ctx, "system", "kyc.status_changed", "user", userID, map[string]any{"from": from, "to": to, "tier": tier, "provider": provider}); err != nil {
			return err
		}
		return tx.Outbox().Enqueue(ctx, "kyc.status_changed", "user", userID, map[string]any{"user_id": userID, "status": to, "tier": tier})
	})
}
func allowed(from, to Status) bool {
	return (from == NotStarted && to == Pending) || (from == Pending && (to == Approved || to == Rejected || to == OnHold)) || (from == OnHold && (to == Pending || to == Rejected)) || (from == Approved && to == Expired)
}

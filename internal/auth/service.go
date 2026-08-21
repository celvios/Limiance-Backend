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
	"github.com/limiance/backend/internal/security/password"
	"github.com/limiance/backend/internal/security/session"
	"github.com/limiance/backend/internal/security/totp"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrVerificationNeeded = errors.New("email verification required")
	ErrAccountFrozen      = errors.New("account is frozen")
	ErrMFARequired        = errors.New("multi-factor authentication required")
	ErrInvalidTOTP        = errors.New("invalid authenticator code")
	ErrTOTPUnavailable    = errors.New("totp is unavailable")
)

type LoginInput struct {
	Email     string `json:"email"`
	Password  string `json:"password"`
	UserAgent string `json:"-"`
	ClientIP  string `json:"-"`
}

type LoginResult struct {
	Token     string
	ExpiresAt time.Time
	MFAToken  string
}

type Principal struct {
	SessionID string
	UserID    string
	UID       int64
	Email     string
}

type Service struct {
	data       *datamanager.Manager
	sessionTTL time.Duration
	totpKey    string
}

func NewService(data *datamanager.Manager, sessionTTL time.Duration, totpKey string) *Service {
	return &Service{data: data, sessionTTL: sessionTTL, totpKey: totpKey}
}

func (s *Service) Login(ctx context.Context, input LoginInput) (LoginResult, error) {
	email := strings.ToLower(strings.TrimSpace(input.Email))
	if _, err := mail.ParseAddress(email); err != nil || input.Password == "" {
		return LoginResult{}, ErrInvalidCredentials
	}

	user, err := s.data.LoginUser(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return LoginResult{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginResult{}, err
	}
	valid, err := password.Verify(user.PasswordHash, input.Password)
	if err != nil || !valid {
		return LoginResult{}, ErrInvalidCredentials
	}
	if user.Status == "frozen" {
		return LoginResult{}, ErrAccountFrozen
	}
	if user.Status != "active" {
		return LoginResult{}, ErrVerificationNeeded
	}
	if user.TOTPEnabled {
		raw, hash, err := session.New()
		if err != nil {
			return LoginResult{}, err
		}
		if err := s.data.CreateMFALoginChallenge(ctx, user.ID, hash, time.Now().UTC().Add(5*time.Minute), sessionMetadata(input.UserAgent, input.ClientIP)); err != nil {
			return LoginResult{}, err
		}
		return LoginResult{MFAToken: raw}, ErrMFARequired
	}
	return s.createSession(ctx, user.ID, "password", sessionMetadata(input.UserAgent, input.ClientIP))
}

func sessionMetadata(userAgent, clientIP string) datamanager.SessionMetadata {
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}
	return datamanager.SessionMetadata{UserAgent: userAgent, ClientIP: clientIP}
}

func (s *Service) createSession(ctx context.Context, userID, method string, meta datamanager.SessionMetadata) (LoginResult, error) {
	raw, hash, err := session.New()
	if err != nil {
		return LoginResult{}, err
	}
	expiresAt := time.Now().UTC().Add(s.sessionTTL)
	if err := s.data.WithinTransaction(ctx, func(tx *datamanager.Transaction) error {
		if _, err := tx.Sessions().Create(ctx, userID, hash, expiresAt, meta); err != nil {
			return err
		}
		return tx.Audit().Record(ctx, "user", "session.created", "user", userID, map[string]string{"method": method})
	}); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Token: raw, ExpiresAt: expiresAt}, nil
}

func (s *Service) EnrollTOTP(ctx context.Context, userID, issuer, email string) (string, string, error) {
	if s.totpKey == "" {
		return "", "", ErrTOTPUnavailable
	}
	secret, err := totp.NewSecret()
	if err != nil {
		return "", "", err
	}
	ciphertext, err := envelope.Seal(s.totpKey, secret)
	if err != nil {
		return "", "", ErrTOTPUnavailable
	}
	if err := s.data.StartTOTPEnrollment(ctx, userID, ciphertext); err != nil {
		return "", "", err
	}
	return secret, "otpauth://totp/" + issuer + ":" + email + "?secret=" + secret + "&issuer=" + issuer + "&algorithm=SHA1&digits=6&period=30", nil
}

func (s *Service) ConfirmTOTP(ctx context.Context, userID, code string) error {
	return s.setTOTP(ctx, userID, code, true)
}
func (s *Service) DisableTOTP(ctx context.Context, userID, code string) error {
	return s.setTOTP(ctx, userID, code, false)
}
func (s *Service) setTOTP(ctx context.Context, userID, code string, enable bool) error {
	if s.totpKey == "" {
		return ErrTOTPUnavailable
	}
	ciphertext, err := s.data.TOTPSecret(ctx, userID, !enable)
	if err != nil {
		return ErrInvalidTOTP
	}
	secret, err := envelope.Open(s.totpKey, ciphertext)
	if err != nil || !totp.Validate(secret, code, time.Now()) {
		return ErrInvalidTOTP
	}
	var changed bool
	if enable {
		changed, err = s.data.EnableTOTP(ctx, userID)
	} else {
		changed, err = s.data.DisableTOTP(ctx, userID)
	}
	if err != nil {
		return err
	}
	if !changed {
		return ErrInvalidTOTP
	}
	return nil
}

func (s *Service) VerifyTOTPLogin(ctx context.Context, mfaToken, code string) (LoginResult, error) {
	if s.totpKey == "" {
		return LoginResult{}, ErrTOTPUnavailable
	}
	userID, meta, err := s.data.ConsumeMFALoginChallenge(ctx, session.Hash(mfaToken))
	if err != nil {
		return LoginResult{}, ErrInvalidTOTP
	}
	ciphertext, err := s.data.TOTPSecret(ctx, userID, true)
	if err != nil {
		return LoginResult{}, ErrInvalidTOTP
	}
	secret, err := envelope.Open(s.totpKey, ciphertext)
	if err != nil || !totp.Validate(secret, code, time.Now()) {
		return LoginResult{}, ErrInvalidTOTP
	}
	return s.createSession(ctx, userID, "password_totp", meta)
}

func (s *Service) Authenticate(ctx context.Context, rawToken string) (Principal, error) {
	if rawToken == "" {
		return Principal{}, ErrInvalidCredentials
	}
	user, err := s.data.ActiveSession(ctx, session.Hash(rawToken))
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrInvalidCredentials
	}
	if err != nil {
		return Principal{}, err
	}
	if user.Status == "frozen" {
		return Principal{}, ErrAccountFrozen
	}
	if user.Status != "active" {
		return Principal{}, ErrVerificationNeeded
	}
	return Principal{SessionID: user.SessionID, UserID: user.UserID, UID: user.UID, Email: user.Email}, nil
}

// Freeze revokes every session, including the caller's current session. It is
// a one-way self-service containment control; restoration must use the
// separate, audited support process.
func (s *Service) Freeze(ctx context.Context, principal Principal) error {
	if principal.UserID == "" {
		return ErrInvalidCredentials
	}
	return s.data.FreezeUser(ctx, principal.UserID)
}

func (s *Service) Logout(ctx context.Context, principal Principal) error {
	return s.data.WithinTransaction(ctx, func(tx *datamanager.Transaction) error {
		if err := tx.Sessions().Revoke(ctx, principal.SessionID); err != nil {
			return err
		}
		return tx.Audit().Record(ctx, "user", "session.revoked", "user", principal.UserID, map[string]string{"method": "logout"})
	})
}

func (s *Service) ActiveSessions(ctx context.Context, principal Principal) ([]datamanager.SessionInfo, error) {
	return s.data.SessionsForUser(ctx, principal.UserID, principal.SessionID)
}

func (s *Service) RevokeSession(ctx context.Context, principal Principal, sessionID string) (bool, error) {
	if sessionID == "" {
		return false, ErrInvalidCredentials
	}
	return s.data.RevokeUserSession(ctx, principal.UserID, sessionID)
}

func (s *Service) SetAntiPhishingCode(ctx context.Context, principal Principal, code string) error {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code != "" {
		if len(code) < 4 || len(code) > 32 {
			return ErrInvalidCredentials
		}
		for _, r := range code {
			if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return ErrInvalidCredentials
			}
		}
	}
	return s.data.SetAntiPhishingCode(ctx, principal.UserID, code)
}

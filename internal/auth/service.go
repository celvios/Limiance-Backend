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
	"github.com/limiance/backend/internal/security/verification"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrVerificationNeeded = errors.New("email verification required")
	ErrAccountFrozen      = errors.New("account is frozen")
	ErrMFARequired        = errors.New("multi-factor authentication required")
	ErrInvalidTOTP        = errors.New("invalid authenticator code")
	ErrTOTPUnavailable    = errors.New("totp is unavailable")
	ErrInvalidReset       = errors.New("invalid or expired password reset code")
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
	data               *datamanager.Manager
	sessionTTL         time.Duration
	totpKey            string
	verificationPepper string
	verificationKey    string
}

func NewService(data *datamanager.Manager, sessionTTL time.Duration, totpKey, verificationPepper, verificationKey string) *Service {
	return &Service{data: data, sessionTTL: sessionTTL, totpKey: totpKey, verificationPepper: verificationPepper, verificationKey: verificationKey}
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
	knownDevice, err := s.data.SessionDeviceKnown(ctx, userID, meta)
	if err != nil {
		return LoginResult{}, err
	}
	expiresAt := time.Now().UTC().Add(s.sessionTTL)
	if err := s.data.WithinTransaction(ctx, func(tx *datamanager.Transaction) error {
		sessionID, err := tx.Sessions().Create(ctx, userID, hash, expiresAt, meta)
		if err != nil {
			return err
		}
		if err := tx.Audit().Record(ctx, "user", "session.created", "user", userID, map[string]string{"method": method}); err != nil {
			return err
		}
		if !knownDevice {
			return tx.Outbox().Enqueue(ctx, "security.new_device_login", "session", sessionID, map[string]string{"user_id": userID, "method": method})
		}
		return nil
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

func (s *Service) RevokeOtherSessions(ctx context.Context, principal Principal) (int64, error) {
	return s.data.RevokeOtherUserSessions(ctx, principal.UserID, principal.SessionID)
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

// RequestPasswordReset always returns nil for unknown/inactive accounts so an
// unauthenticated caller cannot enumerate Limiance customers.
func (s *Service) RequestPasswordReset(ctx context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if _, err := mail.ParseAddress(email); err != nil {
		return nil
	}
	user, err := s.data.PasswordResetUser(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	code, err := verification.NewCode()
	if err != nil {
		return err
	}
	codeHash, err := verification.Hash(s.verificationPepper, code)
	if err != nil {
		return err
	}
	sealedCode, err := envelope.Seal(s.verificationKey, code)
	if err != nil {
		return err
	}
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	return s.data.CreatePasswordResetChallenge(ctx, user.ID, codeHash, expiresAt, map[string]any{"user_id": user.ID, "email": user.Email, "code_ciphertext": sealedCode, "expires_at": expiresAt.Format(time.RFC3339)})
}

func (s *Service) ResetPassword(ctx context.Context, email, code, newPassword string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if _, err := mail.ParseAddress(email); err != nil || !validPassword(newPassword) || len(code) != 6 {
		return ErrInvalidReset
	}
	codeHash, err := verification.Hash(s.verificationPepper, code)
	if err != nil {
		return err
	}
	passwordHash, err := password.Hash(newPassword)
	if err != nil {
		return err
	}
	changed, err := s.data.ResetPassword(ctx, email, codeHash, passwordHash)
	if err != nil {
		return err
	}
	if !changed {
		return ErrInvalidReset
	}
	return nil
}

func (s *Service) StepUp(ctx context.Context, principal Principal, purpose, code string) (string, time.Time, error) {
	if s.totpKey == "" {
		return "", time.Time{}, ErrTOTPUnavailable
	}
	purpose = strings.ToLower(strings.TrimSpace(purpose))
	if len(purpose) < 3 || len(purpose) > 64 || !validStepUpPurpose(purpose) {
		return "", time.Time{}, ErrInvalidCredentials
	}
	ciphertext, err := s.data.TOTPSecret(ctx, principal.UserID, true)
	if err != nil {
		return "", time.Time{}, ErrInvalidTOTP
	}
	secret, err := envelope.Open(s.totpKey, ciphertext)
	if err != nil || !totp.Validate(secret, code, time.Now()) {
		return "", time.Time{}, ErrInvalidTOTP
	}
	raw, hash, err := session.New()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().UTC().Add(5 * time.Minute)
	if err := s.data.CreateMFAStepUpChallenge(ctx, principal.UserID, principal.SessionID, purpose, hash, expiresAt); err != nil {
		return "", time.Time{}, err
	}
	return raw, expiresAt, nil
}

func (s *Service) RequestMFARecovery(ctx context.Context, email, reason string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	reason = strings.TrimSpace(reason)
	if _, err := mail.ParseAddress(email); err != nil || len(reason) < 12 || len(reason) > 1000 {
		return ErrInvalidCredentials
	}
	_, err := s.data.CreateMFARecoveryRequest(ctx, email, reason)
	return err
}

func validPassword(value string) bool {
	if len(value) < 12 || len(value) > 128 {
		return false
	}
	var upper, lower, digit bool
	for _, r := range value {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		}
	}
	return upper && lower && digit
}

func validStepUpPurpose(value string) bool {
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

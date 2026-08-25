// Package datamanager owns all application persistence. Domain services use its
// transaction-scoped repositories; they do not access pgx, pools, or SQL directly.
package datamanager

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/limiance/backend/internal/platform/queue"
)

type Manager struct{ pool *pgxpool.Pool }

var ErrUserNotFreezable = errors.New("user is not eligible to be frozen")

func New(pool *pgxpool.Pool) *Manager { return &Manager{pool: pool} }

func (m *Manager) Ping(ctx context.Context) error { return m.pool.Ping(ctx) }

type UserProfile struct {
	UserID                string `json:"user_id"`
	UID                   int64  `json:"uid"`
	Email                 string `json:"email"`
	DisplayName           string `json:"display_name"`
	KYCStatus             string `json:"kyc_status"`
	KYCTier               int16  `json:"kyc_tier"`
	PreferredCurrency     string `json:"preferred_currency"`
	SecondaryDisplayAsset string `json:"secondary_display_asset"`
	PreferredLanguage     string `json:"preferred_language"`
	PreferredTheme        string `json:"preferred_theme"`
}

type PasswordResetUser struct {
	ID    string
	Email string
}

func (m *Manager) UserProfile(ctx context.Context, userID string) (UserProfile, error) {
	var profile UserProfile
	err := m.pool.QueryRow(ctx, `SELECT u.id::text,u.uid,u.email,u.display_name,COALESCE(k.status::text,'not_started'),COALESCE(k.tier,0),u.preferred_currency,u.secondary_display_asset,u.preferred_language,u.preferred_theme FROM users u LEFT JOIN kyc_profiles k ON k.user_id=u.id WHERE u.id=$1`, userID).Scan(&profile.UserID, &profile.UID, &profile.Email, &profile.DisplayName, &profile.KYCStatus, &profile.KYCTier, &profile.PreferredCurrency, &profile.SecondaryDisplayAsset, &profile.PreferredLanguage, &profile.PreferredTheme)
	return profile, err
}

func (m *Manager) UpdateUserPreferences(ctx context.Context, userID, displayName, currency, secondaryAsset, language, theme string) (UserProfile, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return UserProfile{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `UPDATE users SET display_name=$2,preferred_currency=$3,secondary_display_asset=$4,preferred_language=$5,preferred_theme=$6,updated_at=now() WHERE id=$1 AND status='active'`, userID, displayName, currency, secondaryAsset, language, theme); err != nil {
		return UserProfile{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id,metadata) VALUES($1,'user','user.preferences_updated','user',$1,'{}')`, userID); err != nil {
		return UserProfile{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return UserProfile{}, err
	}
	return m.UserProfile(ctx, userID)
}

func (m *Manager) LoginUser(ctx context.Context, email string) (LoginUser, error) {
	var user LoginUser
	err := m.pool.QueryRow(ctx, `SELECT u.id::text, u.password_hash, u.status, EXISTS (SELECT 1 FROM totp_credentials t WHERE t.user_id = u.id AND t.enabled_at IS NOT NULL AND t.disabled_at IS NULL) FROM users u WHERE u.email = $1`, email).Scan(&user.ID, &user.PasswordHash, &user.Status, &user.TOTPEnabled)
	return user, err
}

func (m *Manager) PasswordResetUser(ctx context.Context, email string) (PasswordResetUser, error) {
	var user PasswordResetUser
	err := m.pool.QueryRow(ctx, `SELECT id::text,email FROM users WHERE email=$1 AND status='active'`, email).Scan(&user.ID, &user.Email)
	return user, err
}

func (m *Manager) CreatePasswordResetChallenge(ctx context.Context, userID string, codeHash []byte, expiresAt time.Time, emailPayload any) error {
	return m.WithinTransaction(ctx, func(tx *Transaction) error {
		if _, err := tx.audit.tx.Exec(ctx, `UPDATE password_reset_challenges SET consumed_at=now() WHERE user_id=$1 AND consumed_at IS NULL`, userID); err != nil {
			return err
		}
		if _, err := tx.audit.tx.Exec(ctx, `INSERT INTO password_reset_challenges (user_id,code_hash,expires_at) VALUES ($1,$2,$3)`, userID, codeHash, expiresAt); err != nil {
			return err
		}
		if err := tx.Audit().Record(ctx, "system", "password_reset.requested", "user", userID, map[string]string{}); err != nil {
			return err
		}
		return tx.Outbox().Enqueue(ctx, "email.password_reset_requested", "user", userID, emailPayload)
	})
}

// ResetPassword consumes a one-time code before changing credentials and
// revoking every active session in the same transaction.
func (m *Manager) ResetPassword(ctx context.Context, email string, suppliedHash []byte, passwordHash string) (bool, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var challengeID, userID string
	var storedHash []byte
	var expiresAt time.Time
	var attempts int16
	err = tx.QueryRow(ctx, `SELECT c.id::text,c.user_id::text,c.code_hash,c.expires_at,c.attempts FROM password_reset_challenges c JOIN users u ON u.id=c.user_id WHERE u.email=$1 AND u.status='active' AND c.consumed_at IS NULL ORDER BY c.created_at DESC LIMIT 1 FOR UPDATE`, email).Scan(&challengeID, &userID, &storedHash, &expiresAt, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if expiresAt.Before(time.Now()) || attempts >= 5 || subtle.ConstantTimeCompare(storedHash, suppliedHash) != 1 {
		_, err = tx.Exec(ctx, `UPDATE password_reset_challenges SET attempts=LEAST(attempts+1,5) WHERE id=$1`, challengeID)
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE password_reset_challenges SET consumed_at=now() WHERE id=$1`, challengeID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET password_hash=$2,updated_at=now() WHERE id=$1`, userID, passwordHash); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, userID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id,metadata) VALUES($1,'user','password_reset.completed','user',$1,'{}')`, userID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_type,aggregate_type,aggregate_id,payload) VALUES('security.password_changed','user',$1,jsonb_build_object('user_id',$1))`, userID); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (m *Manager) CreateMFAStepUpChallenge(ctx context.Context, userID, sessionID, purpose string, tokenHash []byte, expiresAt time.Time) error {
	return m.WithinTransaction(ctx, func(tx *Transaction) error {
		if _, err := tx.audit.tx.Exec(ctx, `INSERT INTO mfa_step_up_challenges (user_id,session_id,purpose,token_hash,expires_at) VALUES ($1,$2,$3,$4,$5)`, userID, sessionID, purpose, tokenHash, expiresAt); err != nil {
			return err
		}
		return tx.Audit().Record(ctx, "user", "mfa.step_up_completed", "session", sessionID, map[string]string{"purpose": purpose})
	})
}

func (m *Manager) ConsumeMFAStepUpChallenge(ctx context.Context, userID, sessionID, purpose string, tokenHash []byte) (bool, error) {
	command, err := m.pool.Exec(ctx, `UPDATE mfa_step_up_challenges SET consumed_at=now() WHERE user_id=$1 AND session_id=$2 AND purpose=$3 AND token_hash=$4 AND consumed_at IS NULL AND expires_at > now()`, userID, sessionID, purpose, tokenHash)
	return command.RowsAffected() == 1, err
}

type APIKey struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Scope       string     `json:"scope"`
	IPWhitelist []string   `json:"ip_whitelist"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

type APIKeyAuthentication struct {
	UserID           string
	UID              int64
	Email            string
	SecretCiphertext string
	IPWhitelist      []string
}

func (m *Manager) APIKeyAuthentication(ctx context.Context, keyHash []byte) (APIKeyAuthentication, error) {
	var key APIKeyAuthentication
	err := m.pool.QueryRow(ctx, `SELECT u.id::text,u.uid,u.email,k.secret_ciphertext,k.ip_whitelist FROM api_keys k JOIN users u ON u.id=k.user_id WHERE k.key_hash=$1 AND k.revoked_at IS NULL AND u.status='active'`, keyHash).Scan(&key.UserID, &key.UID, &key.Email, &key.SecretCiphertext, &key.IPWhitelist)
	return key, err
}

func (m *Manager) CreateAPIKey(ctx context.Context, userID, name, scope string, keyHash []byte, secretCiphertext string, ipWhitelist []string) (APIKey, error) {
	var key APIKey
	err := m.pool.QueryRow(ctx, `INSERT INTO api_keys (user_id,name,key_hash,secret_ciphertext,scope,ip_whitelist) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id::text,name,scope,ip_whitelist,last_used_at,created_at`, userID, name, keyHash, secretCiphertext, scope, ipWhitelist).Scan(&key.ID, &key.Name, &key.Scope, &key.IPWhitelist, &key.LastUsedAt, &key.CreatedAt)
	if err != nil {
		return APIKey{}, err
	}
	err = m.WithinTransaction(ctx, func(tx *Transaction) error {
		return tx.Audit().Record(ctx, "user", "security.api_key_created", "api_key", key.ID, map[string]any{"scope": scope})
	})
	return key, err
}

func (m *Manager) APIKeys(ctx context.Context, userID string) ([]APIKey, error) {
	rows, err := m.pool.Query(ctx, `SELECT id::text,name,scope,ip_whitelist,last_used_at,created_at FROM api_keys WHERE user_id=$1 AND revoked_at IS NULL ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]APIKey, 0)
	for rows.Next() {
		var key APIKey
		if err := rows.Scan(&key.ID, &key.Name, &key.Scope, &key.IPWhitelist, &key.LastUsedAt, &key.CreatedAt); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (m *Manager) RevokeAPIKey(ctx context.Context, userID, keyID string) (bool, error) {
	command, err := m.pool.Exec(ctx, `UPDATE api_keys SET revoked_at=now() WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL`, keyID, userID)
	if err != nil {
		return false, err
	}
	if command.RowsAffected() == 0 {
		return false, nil
	}
	err = m.WithinTransaction(ctx, func(tx *Transaction) error {
		return tx.Audit().Record(ctx, "user", "security.api_key_revoked", "api_key", keyID, nil)
	})
	return err == nil, err
}

// CreateMFARecoveryRequest is intentionally a request for an audited human
// review, never an automatic TOTP reset.
func (m *Manager) CreateMFARecoveryRequest(ctx context.Context, email, reason string) (bool, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var userID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM users WHERE email=$1 AND status='active' FOR UPDATE`, email).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO mfa_recovery_requests(user_id,reason) VALUES($1,$2)`, userID, reason); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id,metadata) VALUES($1,'user','mfa.recovery_requested','user',$1,'{}')`, userID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_type,aggregate_type,aggregate_id,payload) VALUES('security.mfa_recovery_requested','user',$1,jsonb_build_object('user_id',$1))`, userID); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (m *Manager) StartTOTPEnrollment(ctx context.Context, userID, secretCiphertext string) error {
	_, err := m.pool.Exec(ctx, `INSERT INTO totp_credentials (user_id, secret_ciphertext, enabled_at, disabled_at) VALUES ($1,$2,NULL,NULL) ON CONFLICT (user_id) DO UPDATE SET secret_ciphertext=EXCLUDED.secret_ciphertext, enabled_at=NULL, disabled_at=NULL, updated_at=now()`, userID, secretCiphertext)
	return err
}

func (m *Manager) TOTPSecret(ctx context.Context, userID string, enabledOnly bool) (string, error) {
	query := `SELECT secret_ciphertext FROM totp_credentials WHERE user_id=$1 AND disabled_at IS NULL`
	if enabledOnly {
		query += ` AND enabled_at IS NOT NULL`
	}
	var ciphertext string
	err := m.pool.QueryRow(ctx, query, userID).Scan(&ciphertext)
	return ciphertext, err
}

func (m *Manager) EnableTOTP(ctx context.Context, userID string) (bool, error) {
	return m.setTOTPState(ctx, userID, true)
}

func (m *Manager) DisableTOTP(ctx context.Context, userID string) (bool, error) {
	return m.setTOTPState(ctx, userID, false)
}

func (m *Manager) setTOTPState(ctx context.Context, userID string, enabled bool) (bool, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	query := `UPDATE totp_credentials SET enabled_at=now(),updated_at=now() WHERE user_id=$1 AND enabled_at IS NULL AND disabled_at IS NULL`
	eventType := "security.2fa_enabled"
	if !enabled {
		query = `UPDATE totp_credentials SET disabled_at=now(),updated_at=now() WHERE user_id=$1 AND enabled_at IS NOT NULL AND disabled_at IS NULL`
		eventType = "security.2fa_disabled"
	}
	command, err := tx.Exec(ctx, query, userID)
	if err != nil || command.RowsAffected() != 1 {
		return command.RowsAffected() == 1, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id) VALUES($1,'user',$2,'user',$1)`, userID, eventType); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_type,aggregate_type,aggregate_id,payload) VALUES($1,'user',$2,jsonb_build_object('user_id',$2))`, eventType, userID); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (m *Manager) CreateMFALoginChallenge(ctx context.Context, userID string, tokenHash []byte, expiresAt time.Time, meta SessionMetadata) error {
	_, err := m.pool.Exec(ctx, `INSERT INTO mfa_login_challenges (user_id,token_hash,expires_at,user_agent,client_ip) VALUES ($1,$2,$3,$4,NULLIF($5,'')::inet)`, userID, tokenHash, expiresAt, meta.UserAgent, meta.ClientIP)
	return err
}

// ConsumeMFALoginChallenge makes a login challenge single-use before a code is
// verified. An invalid code therefore cannot be retried indefinitely; the user
// must begin a fresh password login.
func (m *Manager) ConsumeMFALoginChallenge(ctx context.Context, tokenHash []byte) (string, SessionMetadata, error) {
	var userID string
	var meta SessionMetadata
	err := m.pool.QueryRow(ctx, `UPDATE mfa_login_challenges SET consumed_at=now() WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at > now() RETURNING user_id::text,user_agent,COALESCE(client_ip::text,'')`, tokenHash).Scan(&userID, &meta.UserAgent, &meta.ClientIP)
	return userID, meta, err
}

type SessionMetadata struct {
	UserAgent string
	ClientIP  string
}

// SessionDeviceKnown is a conservative device fingerprint for notification
// purposes only; it is not an authentication factor. A durable browser/device
// registry belongs with the broader security work, but this prevents routine
// logins from producing a "new device" alert every time.
func (m *Manager) SessionDeviceKnown(ctx context.Context, userID string, meta SessionMetadata) (bool, error) {
	var known bool
	err := m.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE user_id=$1 AND user_agent=$2 AND COALESCE(client_ip::text,'')=$3)`, userID, meta.UserAgent, meta.ClientIP).Scan(&known)
	return known, err
}

type SessionInfo struct {
	ID         string    `json:"id"`
	UserAgent  string    `json:"user_agent"`
	ClientIP   string    `json:"client_ip"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	Current    bool      `json:"current"`
}

func (m *Manager) SessionsForUser(ctx context.Context, userID, currentSessionID string) ([]SessionInfo, error) {
	rows, err := m.pool.Query(ctx, `SELECT id::text,user_agent,COALESCE(client_ip::text,''),created_at,last_seen_at FROM sessions WHERE user_id=$1 AND revoked_at IS NULL AND expires_at > now() ORDER BY last_seen_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SessionInfo, 0)
	for rows.Next() {
		var item SessionInfo
		if err := rows.Scan(&item.ID, &item.UserAgent, &item.ClientIP, &item.CreatedAt, &item.LastSeenAt); err != nil {
			return nil, err
		}
		item.Current = item.ID == currentSessionID
		items = append(items, item)
	}
	return items, rows.Err()
}

func (m *Manager) RevokeUserSession(ctx context.Context, userID, sessionID string) (bool, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	command, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL`, sessionID, userID)
	if err != nil || command.RowsAffected() != 1 {
		return command.RowsAffected() == 1, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events (actor_id,actor_type,action,resource_type,resource_id,metadata) VALUES ($1,'user','session.revoked','session',$2,jsonb_build_object('method','device_management'))`, userID, sessionID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO outbox_events (event_type,aggregate_type,aggregate_id,payload) VALUES ('security.session_revoked','session',$1,jsonb_build_object('user_id',$2))`, sessionID, userID); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (m *Manager) RevokeOtherUserSessions(ctx context.Context, userID, currentSessionID string) (int64, error) {
	command, err := m.pool.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1 AND id<>$2 AND revoked_at IS NULL`, userID, currentSessionID)
	return command.RowsAffected(), err
}

func (m *Manager) EmailVerificationUser(ctx context.Context, email string) (EmailVerificationUser, error) {
	var user EmailVerificationUser
	err := m.pool.QueryRow(ctx, `SELECT id::text, email, status FROM users WHERE email = $1`, email).Scan(&user.ID, &user.Email, &user.Status)
	return user, err
}

func (m *Manager) ActiveSession(ctx context.Context, tokenHash []byte) (SessionUser, error) {
	var session SessionUser
	err := m.pool.QueryRow(ctx, `SELECT s.id::text, u.id::text, u.uid, u.email, u.status FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = $1 AND s.revoked_at IS NULL AND s.expires_at > now()`, tokenHash).Scan(&session.SessionID, &session.UserID, &session.UID, &session.Email, &session.Status)
	return session, err
}

// FreezeUser disables an account and revokes every active session in one
// transaction. The account state, session invalidation, audit record,
// customer-visible alert, and asynchronous security event therefore cannot
// diverge.
func (m *Manager) FreezeUser(ctx context.Context, userID string) error {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	command, err := tx.Exec(ctx, `UPDATE users SET status = 'frozen', updated_at = now() WHERE id = $1 AND status = 'active'`, userID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrUserNotFreezable
	}
	if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, event_type, title, body, priority) VALUES ($1, 'security.account_frozen', 'Account frozen', 'Your account was frozen and all active sessions were signed out. Contact support to restore access.', 'security')`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events (actor_id, actor_type, action, resource_type, resource_id, metadata) VALUES ($1, 'user', 'account.frozen', 'user', $1, '{}'::jsonb)`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO outbox_events (event_type, aggregate_type, aggregate_id, payload) VALUES ('security.account_frozen', 'user', $1, jsonb_build_object('user_id', $1))`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (m *Manager) SetAntiPhishingCode(ctx context.Context, userID, code string) error {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	command, err := tx.Exec(ctx, `UPDATE users SET anti_phishing_code=$2,updated_at=now() WHERE id=$1 AND status='active'`, userID, code)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrUserNotFreezable
	}
	action := "security.anti_phishing_code_set"
	if code == "" {
		action = "security.anti_phishing_code_cleared"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events (actor_id,actor_type,action,resource_type,resource_id,metadata) VALUES ($1,'user',$2,'user',$1,'{}'::jsonb)`, userID, action); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO outbox_events (event_type,aggregate_type,aggregate_id,payload) VALUES ($1,'user',$2,jsonb_build_object('user_id',$2))`, action, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (m *Manager) KYCSessionUser(ctx context.Context, userID string) (KYCUser, error) {
	var user KYCUser
	err := m.pool.QueryRow(ctx, `SELECT u.id::text, u.email, u.status, COALESCE(k.applicant_id, ''), COALESCE(k.status::text, 'not_started') FROM users u LEFT JOIN kyc_profiles k ON k.user_id = u.id WHERE u.id = $1`, userID).Scan(&user.ID, &user.Email, &user.Status, &user.ApplicantID, &user.KYCStatus)
	return user, err
}

// KYCStatus returns the durable, provider-synchronised state for the
// authenticated customer. It deliberately excludes the provider applicant ID
// and review payload, which are internal compliance data.
func (m *Manager) KYCStatus(ctx context.Context, userID string) (KYCProfile, error) {
	var profile KYCProfile
	err := m.pool.QueryRow(ctx, `SELECT COALESCE(k.provider, ''), COALESCE(k.status::text, 'not_started'), COALESCE(k.tier, 0), COALESCE(k.level_name, ''), COALESCE(k.updated_at, u.created_at) FROM users u LEFT JOIN kyc_profiles k ON k.user_id = u.id WHERE u.id = $1`, userID).Scan(&profile.Provider, &profile.Status, &profile.Tier, &profile.LevelName, &profile.UpdatedAt)
	return profile, err
}

func (m *Manager) ResolveTransferRecipient(ctx context.Context, kind, value string) (TransferRecipient, error) {
	var recipient TransferRecipient
	var err error
	switch kind {
	case "uid":
		err = m.pool.QueryRow(ctx, `SELECT u.uid, a.id::text FROM users u JOIN accounts a ON a.user_id=u.id AND a.kind='funding' AND a.status='active' WHERE u.uid::text=$1 AND u.status='active'`, value).Scan(&recipient.UID, &recipient.AccountID)
	case "email":
		err = m.pool.QueryRow(ctx, `SELECT u.uid, a.id::text FROM users u JOIN accounts a ON a.user_id=u.id AND a.kind='funding' AND a.status='active' WHERE u.email=$1 AND u.status='active'`, value).Scan(&recipient.UID, &recipient.AccountID)
	case "phone":
		err = m.pool.QueryRow(ctx, `SELECT u.uid, a.id::text FROM users u JOIN accounts a ON a.user_id=u.id AND a.kind='funding' AND a.status='active' WHERE u.phone_e164=$1 AND u.phone_verified_at IS NOT NULL AND u.status='active'`, value).Scan(&recipient.UID, &recipient.AccountID)
	default:
		return TransferRecipient{}, errors.New("unsupported recipient type")
	}
	return recipient, err
}

// AssetCatalog returns policy catalog entries. It intentionally does not expose
// the assets table: entries there represent individually enabled, operational
// asset/network routes.
func (m *Manager) AssetCatalog(ctx context.Context) ([]CatalogAsset, error) {
	rows, err := m.pool.Query(ctx, `SELECT symbol, display_name, status FROM asset_catalog ORDER BY symbol`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]CatalogAsset, 0)
	for rows.Next() {
		var item CatalogAsset
		if err := rows.Scan(&item.Symbol, &item.DisplayName, &item.Status); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (m *Manager) NetworkCatalog(ctx context.Context) ([]CatalogNetwork, error) {
	rows, err := m.pool.Query(ctx, `SELECT code, display_name, status FROM network_catalog ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]CatalogNetwork, 0)
	for rows.Next() {
		var item CatalogNetwork
		if err := rows.Scan(&item.Code, &item.DisplayName, &item.Status); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type OutboxEvent struct {
	ID            string
	LeaseToken    string
	EventType     string
	AggregateType string
	AggregateID   string
	Payload       json.RawMessage
}

// LeaseOutbox claims a bounded batch. A lease prevents concurrent publisher
// processes from publishing the same row during the lease window; consumers
// must still be idempotent because SQS itself is at-least-once.
func (m *Manager) LeaseOutbox(ctx context.Context, limit int) ([]OutboxEvent, error) {
	if limit < 1 {
		return nil, errors.New("outbox lease limit must be positive")
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT id::text, event_type, aggregate_type, aggregate_id, payload FROM outbox_events WHERE published_at IS NULL AND (lease_expires_at IS NULL OR lease_expires_at < now()) ORDER BY occurred_at LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return nil, err
	}
	events := make([]OutboxEvent, 0, limit)
	for rows.Next() {
		var event OutboxEvent
		if err := rows.Scan(&event.ID, &event.EventType, &event.AggregateType, &event.AggregateID, &event.Payload); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for index := range events {
		if err := tx.QueryRow(ctx, `UPDATE outbox_events SET lease_token = gen_random_uuid(), lease_expires_at = now() + interval '2 minutes', attempts = attempts + 1 WHERE id = $1 RETURNING lease_token::text`, events[index].ID).Scan(&events[index].LeaseToken); err != nil {
			return nil, err
		}
	}
	return events, tx.Commit(ctx)
}

func (m *Manager) MarkOutboxPublished(ctx context.Context, id, leaseToken string) (bool, error) {
	command, err := m.pool.Exec(ctx, `UPDATE outbox_events SET published_at = now(), lease_token = NULL, lease_expires_at = NULL WHERE id = $1 AND lease_token = $2 AND published_at IS NULL`, id, leaseToken)
	return command.RowsAffected() == 1, err
}

func (m *Manager) WithinTransaction(ctx context.Context, fn func(*Transaction) error) error {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(&Transaction{accounts: accountRepository{tx: tx}, verifications: verificationRepository{tx: tx}, kyc: kycRepository{tx: tx}, sessions: sessionRepository{tx: tx}, audit: auditRepository{tx: tx}, outbox: outboxRepository{tx: tx}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type Transaction struct {
	accounts      accountRepository
	verifications verificationRepository
	kyc           kycRepository
	sessions      sessionRepository
	audit         auditRepository
	outbox        outboxRepository
}

func (t *Transaction) Accounts() AccountsRepository          { return t.accounts }
func (t *Transaction) Verifications() VerificationRepository { return t.verifications }
func (t *Transaction) KYC() KYCRepository                    { return t.kyc }
func (t *Transaction) Sessions() SessionsRepository          { return t.sessions }
func (t *Transaction) Audit() AuditRepository                { return t.audit }
func (t *Transaction) Outbox() OutboxRepository              { return t.outbox }

type User struct {
	ID     string
	UID    int64
	Email  string
	Status string
}

type LoginUser struct {
	ID           string
	PasswordHash string
	Status       string
	TOTPEnabled  bool
}

type EmailVerificationUser struct {
	ID     string
	Email  string
	Status string
}

type SessionUser struct {
	SessionID string
	UserID    string
	UID       int64
	Email     string
	Status    string
}

type KYCUser struct {
	ID          string
	Email       string
	Status      string
	ApplicantID string
	KYCStatus   string
}

func (m *Manager) KYCUserByApplicant(ctx context.Context, applicantID string) (KYCUser, error) {
	var user KYCUser
	err := m.pool.QueryRow(ctx, `SELECT u.id::text,u.email,u.status,COALESCE(k.applicant_id,''),COALESCE(k.status::text,'not_started') FROM users u JOIN kyc_profiles k ON k.user_id=u.id WHERE k.applicant_id=$1`, applicantID).Scan(&user.ID, &user.Email, &user.Status, &user.ApplicantID, &user.KYCStatus)
	return user, err
}

type KYCProfile struct {
	Provider  string
	Status    string
	Tier      int16
	LevelName string
	UpdatedAt time.Time
}

type KYCApplication struct {
	UserID       string     `json:"user_id"`
	UID          int64      `json:"uid"`
	Email        string     `json:"email"`
	ApplicantID  string     `json:"applicant_id"`
	Provider     string     `json:"provider"`
	Status       string     `json:"status"`
	Tier         int16      `json:"tier"`
	LevelName    string     `json:"level_name"`
	ReviewAnswer string     `json:"review_answer,omitempty"`
	RejectType   string     `json:"reject_type,omitempty"`
	ReviewedAt   *time.Time `json:"reviewed_at,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func (m *Manager) KYCApplications(ctx context.Context, status string) ([]KYCApplication, error) {
	rows, err := m.pool.Query(ctx, `SELECT u.id::text,u.uid,u.email,COALESCE(k.applicant_id,''),k.provider,k.status::text,k.tier,COALESCE(k.level_name,''),COALESCE(k.review_answer,''),COALESCE(k.review_reject_type,''),k.reviewed_at,k.updated_at FROM kyc_profiles k JOIN users u ON u.id=k.user_id WHERE ($1='' OR k.status::text=$1) ORDER BY k.updated_at ASC`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]KYCApplication, 0)
	for rows.Next() {
		var item KYCApplication
		if err := rows.Scan(&item.UserID, &item.UID, &item.Email, &item.ApplicantID, &item.Provider, &item.Status, &item.Tier, &item.LevelName, &item.ReviewAnswer, &item.RejectType, &item.ReviewedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (m *Manager) ReviewKYC(ctx context.Context, reviewerID, userID, status, answer, reason string, tier int16) error {
	return m.WithinTransaction(ctx, func(tx *Transaction) error {
		command, err := tx.audit.tx.Exec(ctx, `UPDATE kyc_profiles SET status=$2::kyc_status,tier=$3,review_answer=$4,review_reject_type=$5,reviewed_at=now(),updated_at=now() WHERE user_id=$1 AND status IN ('pending','on_hold')`, userID, status, tier, answer, reason)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return errors.New("kyc application is not reviewable")
		}
		if err := tx.Audit().Record(ctx, reviewerID, "kyc.reviewed", "user", userID, map[string]any{"status": status, "tier": tier}); err != nil {
			return err
		}
		return tx.Outbox().Enqueue(ctx, "kyc.status_changed", "user", userID, map[string]any{"user_id": userID, "status": status, "tier": tier})
	})
}

type TransferRecipient struct {
	UID       int64
	AccountID string
}

type TransferInput struct {
	SenderUserID         string
	SourceAccountID      string
	DestinationAccountID string
	AssetSymbol          string
	Network              string
	AmountAtomic         int64
	IdempotencyKey       string
}

type TransferResult struct {
	TransferID string
	JournalID  string
	Duplicate  bool
}

func (m *Manager) CreateInternalTransfer(ctx context.Context, input TransferInput) (TransferResult, error) {
	if input.AmountAtomic <= 0 || input.IdempotencyKey == "" {
		return TransferResult{}, errors.New("invalid transfer input")
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return TransferResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var existing TransferResult
	err = tx.QueryRow(ctx, `SELECT t.id::text, j.id::text FROM transfers t JOIN journals j ON j.transfer_id=t.id WHERE t.idempotency_key=$1`, input.IdempotencyKey).Scan(&existing.TransferID, &existing.JournalID)
	if err == nil {
		existing.Duplicate = true
		return existing, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return TransferResult{}, err
	}

	var assetID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM assets WHERE symbol=$1 AND network=$2 AND status='enabled' ORDER BY id LIMIT 1`, input.AssetSymbol, input.Network).Scan(&assetID)
	if err != nil {
		return TransferResult{}, err
	}
	var sourceOwner, sourceStatus, destinationStatus string
	err = tx.QueryRow(ctx, `SELECT user_id::text, status FROM accounts WHERE id=$1`, input.SourceAccountID).Scan(&sourceOwner, &sourceStatus)
	if err != nil {
		return TransferResult{}, err
	}
	if sourceOwner != input.SenderUserID || sourceStatus != "active" {
		return TransferResult{}, errors.New("source account is not available")
	}
	err = tx.QueryRow(ctx, `SELECT status FROM accounts WHERE id=$1`, input.DestinationAccountID).Scan(&destinationStatus)
	if err != nil {
		return TransferResult{}, err
	}
	if destinationStatus != "active" {
		return TransferResult{}, errors.New("destination account is not available")
	}

	// Serialise balance-changing commands for this account/asset pair. Postings
	// remain immutable; this only prevents two concurrent debits overspending.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, input.SourceAccountID+":"+assetID); err != nil {
		return TransferResult{}, err
	}
	var available int64
	err = tx.QueryRow(ctx, `SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_atomic ELSE -amount_atomic END), 0)::bigint FROM postings WHERE account_id=$1 AND asset_id=$2 AND bucket='available'`, input.SourceAccountID, assetID).Scan(&available)
	if err != nil {
		return TransferResult{}, err
	}
	if available < input.AmountAtomic {
		return TransferResult{}, errors.New("insufficient available balance")
	}

	var result TransferResult
	err = tx.QueryRow(ctx, `INSERT INTO transfers (sender_user_id, source_account_id, destination_account_id, asset_id, amount_atomic, idempotency_key) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id::text`, input.SenderUserID, input.SourceAccountID, input.DestinationAccountID, assetID, input.AmountAtomic, input.IdempotencyKey).Scan(&result.TransferID)
	if err != nil {
		return TransferResult{}, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO journals (idempotency_key, reference_type, reference_id, transfer_id) VALUES ($1,'internal_transfer',$2::text,$2::uuid) RETURNING id::text`, input.IdempotencyKey, result.TransferID).Scan(&result.JournalID)
	if err != nil {
		return TransferResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO postings (journal_id,account_id,asset_id,bucket,direction,amount_atomic) VALUES ($1,$2,$3,'available','debit',$4),($1,$5,$3,'available','credit',$4)`, result.JournalID, input.SourceAccountID, assetID, input.AmountAtomic, input.DestinationAccountID); err != nil {
		return TransferResult{}, err
	}
	metadata, err := json.Marshal(map[string]any{
		"asset_symbol":  input.AssetSymbol,
		"network":       input.Network,
		"amount_atomic": input.AmountAtomic,
	})
	if err != nil {
		return TransferResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events (actor_id,actor_type,action,resource_type,resource_id,metadata) VALUES ($1,'user','transfer.posted','transfer',$2,$3::jsonb)`, input.SenderUserID, result.TransferID, metadata); err != nil {
		return TransferResult{}, err
	}
	payload, err := json.Marshal(map[string]any{
		"transfer_id":    result.TransferID,
		"sender_user_id": input.SenderUserID,
		"asset_symbol":   input.AssetSymbol,
		"network":        input.Network,
		"amount_atomic":  input.AmountAtomic,
	})
	if err != nil {
		return TransferResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events (event_type,aggregate_type,aggregate_id,payload) VALUES ('transfer.completed','transfer',$1,$2::jsonb)`, result.TransferID, payload); err != nil {
		return TransferResult{}, err
	}
	return result, tx.Commit(ctx)
}

// AccountBalance is a ledger-derived balance. Amounts remain decimal strings to
// preserve atomic-unit precision across assets with different decimal places.
type AccountBalance struct {
	AccountID       string `json:"account_id"`
	AccountKind     string `json:"account_kind"`
	AccountName     string `json:"account_name"`
	AssetSymbol     string `json:"asset_symbol"`
	Network         string `json:"network"`
	AvailableAtomic string `json:"available_atomic"`
	HeldAtomic      string `json:"held_atomic"`
	PendingAtomic   string `json:"pending_atomic"`
	LockedAtomic    string `json:"locked_atomic"`
}

type AccountSummary struct {
	ID   string `json:"account_id"`
	Kind string `json:"account_kind"`
	Name string `json:"account_name"`
}

// TransactionHistoryItem is an immutable ledger posting visible to one of a
// user's accounts. Amounts are atomic strings so clients never lose precision.
// A posting, rather than a mutable "transaction" projection, is returned so
// the API remains auditable as new workflow types are introduced.
type TransactionHistoryItem struct {
	PostingID     int64     `json:"posting_id"`
	JournalID     string    `json:"journal_id"`
	ReferenceType string    `json:"reference_type"`
	ReferenceID   string    `json:"reference_id"`
	AccountID     string    `json:"account_id"`
	AccountKind   string    `json:"account_kind"`
	AccountName   string    `json:"account_name"`
	AssetSymbol   string    `json:"asset_symbol"`
	Network       string    `json:"network"`
	Bucket        string    `json:"bucket"`
	Direction     string    `json:"direction"`
	AmountAtomic  string    `json:"amount_atomic"`
	OccurredAt    time.Time `json:"occurred_at"`
}

type Notification struct {
	ID          string          `json:"id"`
	EventType   string          `json:"event_type"`
	ReferenceID string          `json:"reference_id"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	Priority    string          `json:"priority"`
	Metadata    json.RawMessage `json:"metadata"`
	ReadAt      *time.Time      `json:"read_at"`
	CreatedAt   time.Time       `json:"created_at"`
}

type NotificationInput struct {
	UserID      string
	EventType   string
	ReferenceID string
	Title       string
	Body        string
	Priority    string
	Metadata    json.RawMessage
}

type NotificationRecipient struct {
	UserID string
	Email  string
}

// CreateNotification records a notification once for a business event. The
// unique (user_id, event_type, reference_id) key is the idempotency boundary
// for redelivered queue messages.
func (m *Manager) CreateNotification(ctx context.Context, input NotificationInput) (Notification, bool, error) {
	if input.Metadata == nil {
		input.Metadata = json.RawMessage(`{}`)
	}
	var item Notification
	var created bool
	err := m.pool.QueryRow(ctx, `
		INSERT INTO notifications (user_id,event_type,reference_id,title,body,priority,metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb)
		ON CONFLICT (user_id,event_type,reference_id) DO UPDATE SET id=notifications.id
		RETURNING id::text,event_type,reference_id,title,body,priority,metadata,read_at,created_at,(xmax = 0)`,
		input.UserID, input.EventType, input.ReferenceID, input.Title, input.Body, input.Priority, input.Metadata,
	).Scan(&item.ID, &item.EventType, &item.ReferenceID, &item.Title, &item.Body, &item.Priority, &item.Metadata, &item.ReadAt, &item.CreatedAt, &created)
	if err != nil {
		return Notification{}, false, err
	}
	return item, created, nil
}

func (m *Manager) NotificationDeliveryComplete(ctx context.Context, notificationID, channel, destination string) (bool, error) {
	var delivered bool
	err := m.pool.QueryRow(ctx, `SELECT delivered_at IS NOT NULL FROM notification_deliveries WHERE notification_id=$1 AND channel=$2 AND destination=$3`, notificationID, channel, destination).Scan(&delivered)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return delivered, err
}

func (m *Manager) MarkNotificationDeliveryComplete(ctx context.Context, notificationID, channel, destination string) error {
	_, err := m.pool.Exec(ctx, `INSERT INTO notification_deliveries (notification_id,channel,destination,delivered_at,attempts,last_attempt_at) VALUES ($1,$2,$3,now(),1,now()) ON CONFLICT (notification_id,channel,destination) DO UPDATE SET delivered_at=now(),attempts=notification_deliveries.attempts+1,last_attempt_at=now()`, notificationID, channel, destination)
	return err
}

func (m *Manager) RecordNotificationDeliveryAttempt(ctx context.Context, notificationID, channel, destination string) error {
	_, err := m.pool.Exec(ctx, `INSERT INTO notification_deliveries (notification_id,channel,destination,attempts,last_attempt_at) VALUES ($1,$2,$3,1,now()) ON CONFLICT (notification_id,channel,destination) DO UPDATE SET attempts=notification_deliveries.attempts+1,last_attempt_at=now()`, notificationID, channel, destination)
	return err
}

// NotificationRecipients resolves PII only inside the persistence adapter.
// Queue payloads may contain a user id for security events, while financial
// aggregates are joined here so producers do not need to duplicate email
// addresses in every outbox event.
func (m *Manager) NotificationRecipients(ctx context.Context, event queue.Event, payload map[string]any) ([]NotificationRecipient, error) {
	type recipient struct{ userID, email string }
	recipients := make([]recipient, 0, 2)
	add := func(userID, email string) {
		if userID == "" && email == "" {
			return
		}
		for _, existing := range recipients {
			if (userID != "" && existing.userID == userID) || (email != "" && existing.email == email) {
				return
			}
		}
		recipients = append(recipients, recipient{userID: userID, email: email})
	}
	if userID, ok := payload["user_id"].(string); ok && userID != "" {
		var email string
		if err := m.pool.QueryRow(ctx, `SELECT email FROM users WHERE id=$1 AND status='active'`, userID).Scan(&email); err != nil {
			return nil, err
		}
		add(userID, email)
	}
	if event.Type == "email.verification_requested" || event.Type == "email.password_reset_requested" {
		if email, ok := payload["email"].(string); ok && email != "" {
			var userID string
			if err := m.pool.QueryRow(ctx, `SELECT id::text FROM users WHERE email=$1 AND status='active'`, email).Scan(&userID); err != nil {
				return nil, err
			}
			add(userID, email)
		}
	}
	switch event.Type {
	case "deposit.submitted", "deposit.confirming", "deposit.credited", "deposit.failed":
		var userID, email string
		if err := m.pool.QueryRow(ctx, `SELECT u.id::text,u.email FROM deposits d JOIN users u ON u.id=d.user_id WHERE d.id=$1 AND u.status='active'`, event.AggregateID).Scan(&userID, &email); err != nil {
			return nil, err
		}
		add(userID, email)
	case "withdrawal.submitted", "withdrawal.under_review", "withdrawal.approved", "withdrawal.completed", "withdrawal.rejected":
		var userID, email string
		if err := m.pool.QueryRow(ctx, `SELECT u.id::text,u.email FROM withdrawals w JOIN users u ON u.id=w.user_id WHERE w.id=$1 AND u.status='active'`, event.AggregateID).Scan(&userID, &email); err != nil {
			return nil, err
		}
		add(userID, email)
	case "transfer.completed":
		rows, err := m.pool.Query(ctx, `SELECT u.id::text,u.email FROM transfers t JOIN accounts a ON a.id IN (t.source_account_id,t.destination_account_id) JOIN users u ON u.id=a.user_id WHERE t.id=$1 AND u.status='active'`, event.AggregateID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var userID, email string
			if err := rows.Scan(&userID, &email); err != nil {
				return nil, err
			}
			add(userID, email)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	result := make([]NotificationRecipient, 0, len(recipients))
	for _, item := range recipients {
		result = append(result, NotificationRecipient{UserID: item.userID, Email: item.email})
	}
	return result, nil
}

type NotificationDevice struct {
	ID        string    `json:"id"`
	Platform  string    `json:"platform"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type NotificationDeviceRecipient struct {
	Platform string
	Token    string
}

func (m *Manager) Notifications(ctx context.Context, userID string, limit int, createdBefore *time.Time) ([]Notification, error) {
	query := `SELECT id::text,event_type,reference_id,title,body,priority,metadata,read_at,created_at FROM notifications WHERE user_id=$1`
	args := []any{userID}
	if createdBefore != nil {
		query += ` AND created_at < $2`
		args = append(args, *createdBefore)
	}
	query += ` ORDER BY created_at DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit)
	rows, err := m.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Notification, 0)
	for rows.Next() {
		var item Notification
		if err := rows.Scan(&item.ID, &item.EventType, &item.ReferenceID, &item.Title, &item.Body, &item.Priority, &item.Metadata, &item.ReadAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (m *Manager) UnreadNotificationCount(ctx context.Context, userID string) (int64, error) {
	var count int64
	err := m.pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id=$1 AND read_at IS NULL`, userID).Scan(&count)
	return count, err
}

func (m *Manager) MarkNotificationRead(ctx context.Context, userID, notificationID string) (bool, error) {
	command, err := m.pool.Exec(ctx, `UPDATE notifications SET read_at=COALESCE(read_at,now()) WHERE id=$1 AND user_id=$2`, notificationID, userID)
	return command.RowsAffected() == 1, err
}

func (m *Manager) MarkNotificationUnread(ctx context.Context, userID, notificationID string) (bool, error) {
	command, err := m.pool.Exec(ctx, `UPDATE notifications SET read_at=NULL WHERE id=$1 AND user_id=$2`, notificationID, userID)
	return command.RowsAffected() == 1, err
}

func (m *Manager) MarkAllNotificationsRead(ctx context.Context, userID string) (int64, error) {
	command, err := m.pool.Exec(ctx, `UPDATE notifications SET read_at=now() WHERE user_id=$1 AND read_at IS NULL`, userID)
	return command.RowsAffected(), err
}

func (m *Manager) UpsertNotificationDevice(ctx context.Context, userID, platform, token string) (NotificationDevice, error) {
	var device NotificationDevice
	err := m.pool.QueryRow(ctx, `INSERT INTO notification_devices (user_id,platform,token,status) VALUES ($1,$2,$3,'active') ON CONFLICT (token) DO UPDATE SET user_id=EXCLUDED.user_id,platform=EXCLUDED.platform,status='active',updated_at=now() RETURNING id::text,platform,status,created_at`, userID, platform, token).Scan(&device.ID, &device.Platform, &device.Status, &device.CreatedAt)
	return device, err
}

func (m *Manager) NotificationDeviceRecipients(ctx context.Context, userID string) ([]NotificationDeviceRecipient, error) {
	rows, err := m.pool.Query(ctx, `SELECT platform,token FROM notification_devices WHERE user_id=$1 AND status='active'`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	devices := make([]NotificationDeviceRecipient, 0)
	for rows.Next() {
		var device NotificationDeviceRecipient
		if err := rows.Scan(&device.Platform, &device.Token); err != nil {
			return nil, err
		}
		devices = append(devices, device)
	}
	return devices, rows.Err()
}

// AccountBalances returns balances calculated from immutable, posted journal
// entries. It deliberately does not maintain a mutable balance cache.
func (m *Manager) AccountBalances(ctx context.Context, userID string) ([]AccountBalance, error) {
	rows, err := m.pool.Query(ctx, `
		SELECT a.id::text, a.kind::text, a.name, asset.symbol, asset.network,
			COALESCE(SUM(CASE WHEN p.bucket = 'available' AND p.direction = 'credit' THEN p.amount_atomic WHEN p.bucket = 'available' AND p.direction = 'debit' THEN -p.amount_atomic ELSE 0 END), 0)::text,
			COALESCE(SUM(CASE WHEN p.bucket = 'held' AND p.direction = 'credit' THEN p.amount_atomic WHEN p.bucket = 'held' AND p.direction = 'debit' THEN -p.amount_atomic ELSE 0 END), 0)::text,
			COALESCE(SUM(CASE WHEN p.bucket = 'pending' AND p.direction = 'credit' THEN p.amount_atomic WHEN p.bucket = 'pending' AND p.direction = 'debit' THEN -p.amount_atomic ELSE 0 END), 0)::text,
			COALESCE(SUM(CASE WHEN p.bucket = 'locked' AND p.direction = 'credit' THEN p.amount_atomic WHEN p.bucket = 'locked' AND p.direction = 'debit' THEN -p.amount_atomic ELSE 0 END), 0)::text
		FROM accounts a
		JOIN postings p ON p.account_id = a.id
		JOIN journals j ON j.id = p.journal_id AND j.status = 'posted'
		JOIN assets asset ON asset.id = p.asset_id
		WHERE a.user_id = $1 AND a.status = 'active'
		GROUP BY a.id, a.kind, a.name, asset.symbol, asset.network
		ORDER BY a.kind, asset.symbol, asset.network`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	balances := make([]AccountBalance, 0)
	for rows.Next() {
		var balance AccountBalance
		if err := rows.Scan(&balance.AccountID, &balance.AccountKind, &balance.AccountName, &balance.AssetSymbol, &balance.Network, &balance.AvailableAtomic, &balance.HeldAtomic, &balance.PendingAtomic, &balance.LockedAtomic); err != nil {
			return nil, err
		}
		balances = append(balances, balance)
	}
	return balances, rows.Err()
}

func (m *Manager) UserAccounts(ctx context.Context, userID string) ([]AccountSummary, error) {
	rows, err := m.pool.Query(ctx, `
		SELECT id::text, kind::text, name
		FROM accounts
		WHERE user_id = $1 AND status = 'active'
		ORDER BY kind, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	accounts := make([]AccountSummary, 0, 2)
	for rows.Next() {
		var account AccountSummary
		if err := rows.Scan(&account.ID, &account.Kind, &account.Name); err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

// AccountTransactionHistory reads posted ledger entries only. cursor is the
// last posting id from the prior page; it intentionally contains no user data.
func (m *Manager) AccountTransactionHistory(ctx context.Context, userID, accountKind string, limit int, cursor int64) ([]TransactionHistoryItem, error) {
	rows, err := m.pool.Query(ctx, `
		SELECT p.id,j.id::text,j.reference_type,j.reference_id,a.id::text,
			a.kind::text,a.name,asset.symbol,asset.network,p.bucket::text,
			p.direction,p.amount_atomic::text,j.created_at
		FROM postings p
		JOIN journals j ON j.id=p.journal_id AND j.status='posted'
		JOIN accounts a ON a.id=p.account_id
		JOIN assets asset ON asset.id=p.asset_id
		WHERE a.user_id=$1 AND a.status='active'
			AND ($2='' OR a.kind::text=$2)
			AND ($3::bigint=0 OR p.id<$3)
		ORDER BY p.id DESC
		LIMIT $4`, userID, accountKind, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]TransactionHistoryItem, 0)
	for rows.Next() {
		var item TransactionHistoryItem
		if err := rows.Scan(&item.PostingID, &item.JournalID, &item.ReferenceType, &item.ReferenceID, &item.AccountID, &item.AccountKind, &item.AccountName, &item.AssetSymbol, &item.Network, &item.Bucket, &item.Direction, &item.AmountAtomic, &item.OccurredAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type DepositHistoryItem struct {
	ID                    string
	AssetSymbol           string
	Network               string
	AmountAtomic          string
	TransactionHash       string
	Confirmations         int
	ConfirmationsRequired int
	Status                string
	RiskStatus            string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

func (m *Manager) DepositHistory(ctx context.Context, userID string, limit int) ([]DepositHistoryItem, error) {
	rows, err := m.pool.Query(ctx, `SELECT d.id::text,a.symbol,a.network,d.amount_atomic::text,d.transaction_hash,d.confirmations,d.confirmations_required,d.status,d.risk_status,d.first_seen_at,d.updated_at FROM deposits d JOIN assets a ON a.id=d.asset_id WHERE d.user_id=$1 ORDER BY d.first_seen_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]DepositHistoryItem, 0)
	for rows.Next() {
		var item DepositHistoryItem
		if err := rows.Scan(&item.ID, &item.AssetSymbol, &item.Network, &item.AmountAtomic, &item.TransactionHash, &item.Confirmations, &item.ConfirmationsRequired, &item.Status, &item.RiskStatus, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type WithdrawalHistoryItem struct {
	ID           string
	AccountID    string
	AssetSymbol  string
	Network      string
	Address      string
	Tag          string
	AmountAtomic string
	Status       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (m *Manager) WithdrawalHistory(ctx context.Context, userID string, limit int) ([]WithdrawalHistoryItem, error) {
	rows, err := m.pool.Query(ctx, `SELECT w.id::text,w.account_id::text,a.symbol,a.network,wa.address,wa.tag,w.amount_atomic::text,w.status,w.created_at,w.updated_at FROM withdrawals w JOIN assets a ON a.id=w.asset_id JOIN withdrawal_addresses wa ON wa.id=w.withdrawal_address_id WHERE w.user_id=$1 ORDER BY w.created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]WithdrawalHistoryItem, 0)
	for rows.Next() {
		var item WithdrawalHistoryItem
		if err := rows.Scan(&item.ID, &item.AccountID, &item.AssetSymbol, &item.Network, &item.Address, &item.Tag, &item.AmountAtomic, &item.Status, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type WithdrawalAddress struct {
	ID          string
	AssetSymbol string
	Network     string
	Address     string
	Tag         string
	Label       string
	Status      string
	ActivatedAt *time.Time
	CreatedAt   time.Time
}

func (m *Manager) WithdrawalAddresses(ctx context.Context, userID string) ([]WithdrawalAddress, error) {
	if _, err := m.pool.Exec(ctx, `UPDATE withdrawal_addresses SET status='active' WHERE user_id=$1 AND status='pending' AND activated_at <= now()`, userID); err != nil {
		return nil, err
	}
	rows, err := m.pool.Query(ctx, `SELECT w.id::text,a.symbol,a.network,w.address,w.tag,w.label,w.status,w.activated_at,w.created_at FROM withdrawal_addresses w JOIN assets a ON a.id=w.asset_id WHERE w.user_id=$1 ORDER BY w.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]WithdrawalAddress, 0)
	for rows.Next() {
		var item WithdrawalAddress
		if err := rows.Scan(&item.ID, &item.AssetSymbol, &item.Network, &item.Address, &item.Tag, &item.Label, &item.Status, &item.ActivatedAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

var ErrWithdrawalAddressUnavailable = errors.New("withdrawal address unavailable")

func (m *Manager) AddWithdrawalAddress(ctx context.Context, userID, symbol, network, address, tag, label string, cooldown time.Duration) (WithdrawalAddress, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return WithdrawalAddress{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var assetID string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM assets WHERE symbol=$1 AND network=$2 AND status='enabled'`, symbol, network).Scan(&assetID); errors.Is(err, pgx.ErrNoRows) {
		return WithdrawalAddress{}, ErrWithdrawalAddressUnavailable
	} else if err != nil {
		return WithdrawalAddress{}, err
	}
	var item WithdrawalAddress
	activation := time.Now().UTC().Add(cooldown)
	err = tx.QueryRow(ctx, `INSERT INTO withdrawal_addresses (user_id,asset_id,address,tag,label,status,activated_at) VALUES ($1,$2,$3,$4,$5,'pending',$6) ON CONFLICT (user_id,asset_id,address,tag) DO UPDATE SET label=EXCLUDED.label WHERE withdrawal_addresses.status='pending' RETURNING id::text,$7,$8,address,tag,label,status,activated_at,created_at`, userID, assetID, address, tag, label, activation, symbol, network).Scan(&item.ID, &item.AssetSymbol, &item.Network, &item.Address, &item.Tag, &item.Label, &item.Status, &item.ActivatedAt, &item.CreatedAt)
	if err != nil {
		return WithdrawalAddress{}, err
	}
	payload, err := json.Marshal(map[string]string{"asset_symbol": symbol, "network": network, "label": label})
	if err != nil {
		return WithdrawalAddress{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events (actor_id,actor_type,action,resource_type,resource_id,metadata) VALUES ($1,'user','withdrawal.address_added','withdrawal_address',$2,$3::jsonb)`, userID, item.ID, payload); err != nil {
		return WithdrawalAddress{}, err
	}
	return item, tx.Commit(ctx)
}

func (m *Manager) DisableWithdrawalAddress(ctx context.Context, userID, addressID string) (bool, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	command, err := tx.Exec(ctx, `UPDATE withdrawal_addresses SET status='disabled' WHERE id=$1 AND user_id=$2 AND status IN ('pending','active')`, addressID, userID)
	if err != nil {
		return false, err
	}
	if command.RowsAffected() != 1 {
		return false, tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events (actor_id,actor_type,action,resource_type,resource_id) VALUES ($1,'user','withdrawal.address_disabled','withdrawal_address',$2)`, userID, addressID); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

type DepositAsset struct {
	ID             string
	Symbol         string
	Network        string
	CustodyAssetID string
}

type CustodyWallet struct {
	ID              string
	ExternalVaultID string
	Status          string
}

type DepositAddress struct {
	ID                string `json:"id"`
	AssetSymbol       string `json:"asset_symbol"`
	Network           string `json:"network"`
	Address           string `json:"address"`
	Tag               string `json:"tag"`
	ProviderAddressID string `json:"provider_address_id"`
	Status            string `json:"status"`
}

// DepositAsset returns only an individually enabled route with a verified
// custody provider identifier. Catalog approval alone is never enough.
func (m *Manager) DepositAsset(ctx context.Context, symbol, network string) (DepositAsset, error) {
	var asset DepositAsset
	err := m.pool.QueryRow(ctx, `SELECT id::text, symbol, network, custody_asset_id FROM assets WHERE symbol = $1 AND network = $2 AND status = 'enabled' AND custody_asset_id <> ''`, symbol, network).Scan(&asset.ID, &asset.Symbol, &asset.Network, &asset.CustodyAssetID)
	return asset, err
}

func (m *Manager) ActiveDepositAddress(ctx context.Context, userID, assetID, provider string) (DepositAddress, error) {
	var address DepositAddress
	err := m.pool.QueryRow(ctx, `SELECT d.id::text, a.symbol, a.network, d.address, d.tag, COALESCE(d.provider_address_id, ''), d.status FROM deposit_addresses d JOIN assets a ON a.id = d.asset_id JOIN custody_wallets c ON c.id=d.custody_wallet_id WHERE d.user_id = $1 AND d.asset_id = $2 AND c.provider=$3 AND d.status = 'active'`, userID, assetID, provider).Scan(&address.ID, &address.AssetSymbol, &address.Network, &address.Address, &address.Tag, &address.ProviderAddressID, &address.Status)
	return address, err
}

func (m *Manager) CustodyWallet(ctx context.Context, userID, provider string) (CustodyWallet, error) {
	var wallet CustodyWallet
	err := m.pool.QueryRow(ctx, `SELECT id::text, COALESCE(external_vault_id, ''), status FROM custody_wallets WHERE user_id = $1 AND provider = $2`, userID, provider).Scan(&wallet.ID, &wallet.ExternalVaultID, &wallet.Status)
	return wallet, err
}

// SaveCustodyWallet binds an externally created custody wallet to a user. The
// database remains the mapping source of truth; any provider receives only the
// opaque user UUID as customer reference.
func (m *Manager) SaveCustodyWallet(ctx context.Context, userID, provider, externalVaultID string) (CustodyWallet, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CustodyWallet{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var wallet CustodyWallet
	err = tx.QueryRow(ctx, `INSERT INTO custody_wallets (user_id, provider, external_vault_id, status) VALUES ($1, $2, $3, 'active') ON CONFLICT (user_id, provider) DO UPDATE SET external_vault_id = EXCLUDED.external_vault_id, status = 'active' WHERE custody_wallets.external_vault_id IS NULL OR custody_wallets.external_vault_id = EXCLUDED.external_vault_id RETURNING id::text, external_vault_id, status`, userID, provider, externalVaultID).Scan(&wallet.ID, &wallet.ExternalVaultID, &wallet.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT id::text, COALESCE(external_vault_id, ''), status FROM custody_wallets WHERE user_id = $1 AND provider = $2`, userID, provider).Scan(&wallet.ID, &wallet.ExternalVaultID, &wallet.Status)
	}
	if err != nil {
		return CustodyWallet{}, err
	}
	payload, err := json.Marshal(map[string]string{"external_vault_id": wallet.ExternalVaultID})
	if err != nil {
		return CustodyWallet{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events (actor_id, actor_type, action, resource_type, resource_id, metadata) VALUES ($1, 'user', 'custody.wallet_linked', 'custody_wallet', $2, $3::jsonb)`, userID, wallet.ID, payload); err != nil {
		return CustodyWallet{}, err
	}
	return wallet, tx.Commit(ctx)
}

// SaveDepositAddress stores an address only after the custody provider has
// returned it. The active unique index makes repeated requests converge to
// one address even if requests race.
func (m *Manager) SaveDepositAddress(ctx context.Context, userID string, asset DepositAsset, custodyWalletID, providerAddressID, address, tag string) (DepositAddress, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return DepositAddress{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var saved DepositAddress
	created := true
	err = tx.QueryRow(ctx, `INSERT INTO deposit_addresses (user_id, asset_id, custody_wallet_id, provider_address_id, address, tag) VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6) ON CONFLICT (user_id, asset_id, custody_wallet_id) WHERE status = 'active' DO NOTHING RETURNING id::text, $7, $8, address, tag, COALESCE(provider_address_id, ''), status`, userID, asset.ID, custodyWalletID, providerAddressID, address, tag, asset.Symbol, asset.Network).Scan(&saved.ID, &saved.AssetSymbol, &saved.Network, &saved.Address, &saved.Tag, &saved.ProviderAddressID, &saved.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		created = false
		err = tx.QueryRow(ctx, `SELECT d.id::text, $4, $5, d.address, d.tag, COALESCE(d.provider_address_id, ''), d.status FROM deposit_addresses d WHERE d.user_id = $1 AND d.asset_id = $2 AND d.custody_wallet_id=$3 AND d.status = 'active'`, userID, asset.ID, custodyWalletID, asset.Symbol, asset.Network).Scan(&saved.ID, &saved.AssetSymbol, &saved.Network, &saved.Address, &saved.Tag, &saved.ProviderAddressID, &saved.Status)
	}
	if err != nil {
		return DepositAddress{}, err
	}
	if !created {
		return saved, tx.Commit(ctx)
	}
	payload, err := json.Marshal(map[string]string{"asset_symbol": asset.Symbol, "network": asset.Network})
	if err != nil {
		return DepositAddress{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events (actor_id, actor_type, action, resource_type, resource_id, metadata) VALUES ($1, 'user', 'deposit.address_issued', 'deposit_address', $2, $3::jsonb)`, userID, saved.ID, payload); err != nil {
		return DepositAddress{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events (event_type, aggregate_type, aggregate_id, payload) VALUES ('deposit.address_issued', 'deposit_address', $1, $2::jsonb)`, saved.ID, payload); err != nil {
		return DepositAddress{}, err
	}
	return saved, tx.Commit(ctx)
}

// RecordWebhookReceipt provides durable, provider-agnostic webhook
// deduplication. A verified event is queued for a worker; webhook intake never
// performs confirmation logic or ledger posting synchronously.
func (m *Manager) RecordWebhookReceipt(ctx context.Context, provider string, rawPayload []byte) (bool, error) {
	digest := sha256.Sum256(rawPayload)
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var receiptID string
	err = tx.QueryRow(ctx, `INSERT INTO webhook_receipts (provider, payload_digest, payload) VALUES ($1, $2, $3::jsonb) ON CONFLICT (provider, payload_digest) DO NOTHING RETURNING id::text`, provider, hex.EncodeToString(digest[:]), rawPayload).Scan(&receiptID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	payload, err := json.Marshal(map[string]string{"provider": provider, "receipt_id": receiptID, "payload_digest": hex.EncodeToString(digest[:])})
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events (event_type, aggregate_type, aggregate_id, payload) VALUES ('custody.webhook_received', 'webhook_receipt', $1, $2::jsonb)`, receiptID, payload); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (m *Manager) WebhookPayload(ctx context.Context, receiptID string) ([]byte, bool, error) {
	var payload []byte
	var processedAt *time.Time
	err := m.pool.QueryRow(ctx, `SELECT payload, processed_at FROM webhook_receipts WHERE id = $1 AND provider = 'fireblocks'`, receiptID).Scan(&payload, &processedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return payload, processedAt != nil, nil
}

type DepositObservation struct {
	ProviderTransactionID string
	CustodyAssetID        string
	DestinationAddress    string
	DestinationTag        string
	TransactionHash       string
	BlockchainIndex       string
	AmountAtomic          string
	Confirmations         int
	BlockHash             string
	BlockHeight           string
	Status                string
}

// DepositReconciliation is the immutable deposit context needed by a chain
// reconciliation worker. It deliberately contains no user PII.
type DepositReconciliation struct {
	ProviderTransactionID string
	CustodyAssetID        string
	Network               string
	DestinationAddress    string
	DestinationTag        string
	TransactionHash       string
	AmountAtomic          string
}

// DepositForReconciliation finds a previously observed deposit by its on-chain
// transaction hash. A reconciliation worker must still validate the receipt
// against every returned field before it calls ApplyDepositObservation.
func (m *Manager) DepositForReconciliation(ctx context.Context, transactionHash string) (DepositReconciliation, error) {
	var deposit DepositReconciliation
	err := m.pool.QueryRow(ctx, `SELECT d.provider_transaction_id, a.custody_asset_id, a.network, da.address, da.tag, d.transaction_hash, d.amount_atomic::text
		FROM deposits d
		JOIN assets a ON a.id = d.asset_id
		JOIN deposit_addresses da ON da.id = d.deposit_address_id
		WHERE d.transaction_hash = $1`, transactionHash).Scan(
		&deposit.ProviderTransactionID,
		&deposit.CustodyAssetID,
		&deposit.Network,
		&deposit.DestinationAddress,
		&deposit.DestinationTag,
		&deposit.TransactionHash,
		&deposit.AmountAtomic,
	)
	return deposit, err
}

func (m *Manager) DepositAssetDecimals(ctx context.Context, custodyAssetID, address, tag string) (int16, bool, error) {
	var decimals int16
	err := m.pool.QueryRow(ctx, `SELECT a.decimals FROM deposit_addresses d JOIN assets a ON a.id = d.asset_id WHERE d.status = 'active' AND d.address = $1 AND d.tag = $2 AND a.status = 'enabled' AND a.custody_asset_id = $3`, address, tag, custodyAssetID).Scan(&decimals)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return decimals, err == nil, err
}

// ApplyDepositObservation persists a confirmed chain observation. It never
// creates ledger postings: risk approval and a distinct credit command are the
// only path from pending_risk_review to credited.
func (m *Manager) ApplyDepositObservation(ctx context.Context, event DepositObservation) (bool, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var userID, assetID, addressID, walletID string
	var required int
	err = tx.QueryRow(ctx, `SELECT d.user_id::text, a.id::text, d.id::text, d.custody_wallet_id::text, a.confirmations_required FROM deposit_addresses d JOIN assets a ON a.id = d.asset_id WHERE d.status = 'active' AND d.address = $1 AND d.tag = $2 AND a.status = 'enabled' AND a.custody_asset_id = $3`, event.DestinationAddress, event.DestinationTag, event.CustodyAssetID).Scan(&userID, &assetID, &addressID, &walletID, &required)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	var existingDepositID, existingDepositStatus string
	err = tx.QueryRow(ctx, `SELECT id::text,status FROM deposits WHERE provider_transaction_id=$1 AND blockchain_index=$2`, event.ProviderTransactionID, event.BlockchainIndex).Scan(&existingDepositID, &existingDepositStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		existingDepositID, existingDepositStatus = "", ""
	} else if err != nil {
		return false, err
	}
	status := "confirming"
	if event.Status == "FAILED" || event.Status == "REJECTED" {
		status = "reorged"
	} else if event.Confirmations >= required {
		status = "pending_risk_review"
	}
	command, err := tx.Exec(ctx, `INSERT INTO deposits (user_id, asset_id, deposit_address_id, custody_wallet_id, provider_transaction_id, transaction_hash, blockchain_index, amount_atomic, confirmations, confirmations_required, status, block_hash, block_height) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT (provider_transaction_id, blockchain_index) DO UPDATE SET confirmations = GREATEST(deposits.confirmations, EXCLUDED.confirmations), transaction_hash = CASE WHEN EXCLUDED.transaction_hash <> '' THEN EXCLUDED.transaction_hash ELSE deposits.transaction_hash END, block_hash = CASE WHEN EXCLUDED.block_hash <> '' THEN EXCLUDED.block_hash ELSE deposits.block_hash END, block_height = CASE WHEN EXCLUDED.block_height <> '' THEN EXCLUDED.block_height ELSE deposits.block_height END, status = CASE WHEN deposits.status IN ('credited','reorged','rejected') THEN deposits.status ELSE EXCLUDED.status END, updated_at = now()`, userID, assetID, addressID, walletID, event.ProviderTransactionID, event.TransactionHash, event.BlockchainIndex, event.AmountAtomic, event.Confirmations, required, status, event.BlockHash, event.BlockHeight)
	if err != nil {
		return false, err
	}
	if existingDepositID == "" || (status == "confirming" && existingDepositStatus != "credited" && existingDepositStatus != "reorged" && existingDepositStatus != "rejected") || event.Status == "FAILED" || event.Status == "REJECTED" {
		eventType := "deposit.confirming"
		if event.Status == "FAILED" || event.Status == "REJECTED" {
			eventType = "deposit.failed"
		} else if existingDepositID == "" {
			eventType = "deposit.submitted"
		}
		payload, err := json.Marshal(map[string]any{
			"user_id":       userID,
			"amount_atomic": event.AmountAtomic,
			"confirmations": event.Confirmations,
		})
		if err != nil {
			return false, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO outbox_events (event_type,aggregate_type,aggregate_id,payload) SELECT $1,'deposit',d.id,$2::jsonb FROM deposits d WHERE d.provider_transaction_id=$3 AND d.blockchain_index=$4`, eventType, payload, event.ProviderTransactionID, event.BlockchainIndex); err != nil {
			return false, err
		}
	}
	// A confirmed, unflagged deposit is credited automatically. Manual review is
	// reserved for a risk worker that explicitly changes risk_status to pending
	// or rejected before this transition. This keeps the normal path scalable
	// while preserving an immutable, auditable credit journal.
	if status == "pending_risk_review" {
		if err := m.autoCreditConfirmedDeposit(ctx, tx, event.ProviderTransactionID, event.BlockchainIndex); err != nil {
			return false, err
		}
	}
	return command.RowsAffected() > 0, tx.Commit(ctx)
}

func (m *Manager) autoCreditConfirmedDeposit(ctx context.Context, tx pgx.Tx, providerTransactionID, blockchainIndex string) error {
	var depositID, userID, assetID, amount, status, riskStatus string
	err := tx.QueryRow(ctx, `SELECT id::text,user_id::text,asset_id::text,amount_atomic::text,status,risk_status
		FROM deposits WHERE provider_transaction_id=$1 AND blockchain_index=$2 FOR UPDATE`, providerTransactionID, blockchainIndex).Scan(&depositID, &userID, &assetID, &amount, &status, &riskStatus)
	if err != nil {
		return err
	}
	if status != "pending_risk_review" || riskStatus != "not_started" {
		return nil
	}
	var destinationAccountID, symbol, network string
	err = tx.QueryRow(ctx, `SELECT a.id::text,asset.symbol,asset.network FROM accounts a JOIN assets asset ON asset.id=$2 WHERE a.user_id=$1 AND a.kind='funding' AND a.status='active'`, userID, assetID).Scan(&destinationAccountID, &symbol, &network)
	if err != nil {
		return err
	}
	systemName := "custody-clearing-" + assetID
	var systemAccountID string
	if err = tx.QueryRow(ctx, `INSERT INTO accounts (user_id,kind,name) VALUES (NULL,'system',$1) ON CONFLICT (name) WHERE kind='system' DO UPDATE SET name=EXCLUDED.name RETURNING id::text`, systemName).Scan(&systemAccountID); err != nil {
		return err
	}
	var journalID string
	if err = tx.QueryRow(ctx, `INSERT INTO journals (idempotency_key,reference_type,reference_id) VALUES ($1,'deposit_credit',$2) RETURNING id::text`, "deposit-credit-"+depositID, depositID).Scan(&journalID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO postings (journal_id,account_id,asset_id,bucket,direction,amount_atomic) VALUES ($1,$2,$3,'available','debit',$4),($1,$5,$3,'available','credit',$4)`, journalID, systemAccountID, assetID, amount, destinationAccountID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE deposits SET status='credited',risk_status='approved',credited_at=now(),updated_at=now() WHERE id=$1 AND status='pending_risk_review' AND risk_status='not_started'`, depositID); err != nil {
		return err
	}
	metadata, err := json.Marshal(map[string]string{"reason": "automatic chain-confirmation credit", "asset_symbol": symbol, "network": network, "amount_atomic": amount})
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events (actor_type,action,resource_type,resource_id,reason,metadata) VALUES ('system','deposit.auto_credited','deposit',$1,'required blockchain confirmations reached',$2::jsonb)`, depositID, metadata); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events (event_type,aggregate_type,aggregate_id,payload) VALUES ('deposit.credited','deposit',$1,$2::jsonb)`, depositID, metadata)
	return err
}

func (m *Manager) MarkWebhookProcessed(ctx context.Context, receiptID string) error {
	_, err := m.pool.Exec(ctx, `UPDATE webhook_receipts SET processed_at = now() WHERE id = $1 AND processed_at IS NULL`, receiptID)
	return err
}

func (m *Manager) HasRole(ctx context.Context, userID, role string) (bool, error) {
	var exists bool
	err := m.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_roles WHERE user_id = $1 AND role = $2)`, userID, role).Scan(&exists)
	return exists, err
}

type UserRole struct {
	Role      string    `json:"role"`
	GrantedAt time.Time `json:"granted_at"`
}

func (m *Manager) UserRoles(ctx context.Context, userID string) ([]UserRole, error) {
	rows, err := m.pool.Query(ctx, `SELECT role, granted_at FROM user_roles WHERE user_id=$1 ORDER BY role`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roles := make([]UserRole, 0)
	for rows.Next() {
		var role UserRole
		if err := rows.Scan(&role.Role, &role.GrantedAt); err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}

// SetUserRole is intentionally atomic with the audit and outbox events. It
// refuses to remove the final platform administrator so the internal control
// plane cannot be permanently locked out through a single request.
func (m *Manager) SetUserRole(ctx context.Context, actorID, userID, role string, grant bool) (bool, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var administrator bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_roles WHERE user_id=$1 AND role='platform_administrator')`, actorID).Scan(&administrator); err != nil {
		return false, err
	}
	if !administrator {
		return false, ErrAdministratorRoleRequired
	}
	var userExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id=$1)`, userID).Scan(&userExists); err != nil {
		return false, err
	}
	if !userExists {
		return false, nil
	}
	if !grant && role == "platform_administrator" {
		var count int
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('limiance:platform_administrator_role'))`); err != nil {
			return false, err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM user_roles WHERE role='platform_administrator'`).Scan(&count); err != nil {
			return false, err
		}
		if count <= 1 {
			return false, ErrLastAdministrator
		}
	}
	var changed bool
	if grant {
		command, err := tx.Exec(ctx, `INSERT INTO user_roles (user_id,role) VALUES ($1,$2) ON CONFLICT DO NOTHING`, userID, role)
		if err != nil {
			return false, err
		}
		changed = command.RowsAffected() == 1
	} else {
		command, err := tx.Exec(ctx, `DELETE FROM user_roles WHERE user_id=$1 AND role=$2`, userID, role)
		if err != nil {
			return false, err
		}
		changed = command.RowsAffected() == 1
	}
	if !changed {
		return false, tx.Commit(ctx)
	}
	action := "admin.role_granted"
	if !grant {
		action = "admin.role_revoked"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events (actor_id,actor_type,action,resource_type,resource_id,metadata) VALUES ($1,'admin',$2,'user',$3,jsonb_build_object('role',$4))`, actorID, action, userID, role); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO outbox_events (event_type,aggregate_type,aggregate_id,payload) VALUES ($1,'user',$2,jsonb_build_object('user_id',$2,'role',$3))`, action, userID, role); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (m *Manager) SetWithdrawalsEnabled(ctx context.Context, actorID string, enabled bool) error {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_roles WHERE user_id=$1 AND role='platform_administrator')`, actorID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return ErrWithdrawalsDisabled
	}
	if _, err = tx.Exec(ctx, `UPDATE operational_controls SET enabled=$1,updated_by=$2,updated_at=now() WHERE control_key='withdrawals_enabled'`, enabled, actorID); err != nil {
		return err
	}
	action := "withdrawals.enabled"
	if !enabled {
		action = "withdrawals.kill_switch_enabled"
	}
	payload, err := json.Marshal(map[string]bool{"enabled": enabled})
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events (actor_id,actor_type,action,resource_type,resource_id,metadata) VALUES ($1,'admin',$2,'operational_control','withdrawals_enabled',$3::jsonb)`, actorID, action, payload); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events (event_type,aggregate_type,aggregate_id,payload) VALUES ($1,'operational_control','withdrawals_enabled',$2::jsonb)`, action, payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (m *Manager) WithdrawalsEnabled(ctx context.Context) (bool, error) {
	var enabled bool
	err := m.pool.QueryRow(ctx, `SELECT enabled FROM operational_controls WHERE control_key='withdrawals_enabled'`).Scan(&enabled)
	return enabled, err
}

var ErrDepositNotCreditable = errors.New("deposit is not creditable")
var ErrWithdrawalNotAllowed = errors.New("withdrawal is not allowed")
var ErrWithdrawalNotApprovable = errors.New("withdrawal is not approvable")
var ErrWithdrawalsDisabled = errors.New("withdrawals are disabled")
var ErrAdministratorRoleRequired = errors.New("platform administrator role is required")
var ErrLastAdministrator = errors.New("cannot remove final platform administrator")

type WithdrawalApprovalResult struct {
	WithdrawalID  string
	ApprovalCount int
	Status        string
}

// ApproveWithdrawal implements the Phase 1 dual-control gate. It requires a
// treasury approver role and two distinct approvers before a request can move
// from pending_approval to approved. Custody submission remains a separate,
// later command and cannot bypass this state transition.
func (m *Manager) ApproveWithdrawal(ctx context.Context, approverID, withdrawalID, reason string) (WithdrawalApprovalResult, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return WithdrawalApprovalResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var withdrawalsEnabled bool
	if err = tx.QueryRow(ctx, `SELECT enabled FROM operational_controls WHERE control_key='withdrawals_enabled' FOR SHARE`).Scan(&withdrawalsEnabled); err != nil {
		return WithdrawalApprovalResult{}, err
	}
	if !withdrawalsEnabled {
		return WithdrawalApprovalResult{}, ErrWithdrawalsDisabled
	}
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_roles WHERE user_id=$1 AND role='treasury_approver')`, approverID).Scan(&allowed); err != nil {
		return WithdrawalApprovalResult{}, err
	}
	if !allowed {
		return WithdrawalApprovalResult{}, ErrWithdrawalNotApprovable
	}
	var status, customerStatus string
	if err = tx.QueryRow(ctx, `SELECT w.status, u.status FROM withdrawals w JOIN users u ON u.id = w.user_id WHERE w.id=$1 FOR UPDATE OF w, u`, withdrawalID).Scan(&status, &customerStatus); errors.Is(err, pgx.ErrNoRows) || status != "pending_approval" || customerStatus != "active" {
		return WithdrawalApprovalResult{}, ErrWithdrawalNotApprovable
	}
	if err != nil {
		return WithdrawalApprovalResult{}, err
	}
	command, err := tx.Exec(ctx, `INSERT INTO withdrawal_approvals(withdrawal_id,approver_user_id,reason) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, withdrawalID, approverID, reason)
	if err != nil {
		return WithdrawalApprovalResult{}, err
	}
	if command.RowsAffected() != 1 {
		return WithdrawalApprovalResult{}, ErrWithdrawalNotApprovable
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM withdrawal_approvals WHERE withdrawal_id=$1`, withdrawalID).Scan(&count); err != nil {
		return WithdrawalApprovalResult{}, err
	}
	if count >= 2 {
		status = "approved"
		if _, err = tx.Exec(ctx, `UPDATE withdrawals SET status='approved',updated_at=now() WHERE id=$1 AND status='pending_approval'`, withdrawalID); err != nil {
			return WithdrawalApprovalResult{}, err
		}
	}
	metadata, err := json.Marshal(map[string]any{"reason": reason, "approval_count": count})
	if err != nil {
		return WithdrawalApprovalResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id,metadata) VALUES($1,'user','withdrawal.approved','withdrawal',$2,$3::jsonb)`, approverID, withdrawalID, metadata); err != nil {
		return WithdrawalApprovalResult{}, err
	}
	if status == "approved" {
		if _, err = tx.Exec(ctx, `INSERT INTO outbox_events (event_type,aggregate_type,aggregate_id,payload) VALUES ('withdrawal.approved','withdrawal',$1,$2::jsonb)`, withdrawalID, metadata); err != nil {
			return WithdrawalApprovalResult{}, err
		}
	}
	return WithdrawalApprovalResult{WithdrawalID: withdrawalID, ApprovalCount: count, Status: status}, tx.Commit(ctx)
}

// CreditApprovedDeposit is the only ledger path for a deposit. Callers must
// first prove a compliance role; this method then locks state, requires an
// approved KYC profile, and posts one balanced journal atomically.
func (m *Manager) CreditApprovedDeposit(ctx context.Context, reviewerID, depositID, reason string) (TransferResult, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return TransferResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_roles WHERE user_id = $1 AND role = 'compliance')`, reviewerID).Scan(&allowed); err != nil || !allowed {
		if err != nil {
			return TransferResult{}, err
		}
		return TransferResult{}, ErrDepositNotCreditable
	}
	var userID, assetID, amount, status, riskStatus string
	err = tx.QueryRow(ctx, `SELECT d.user_id::text, d.asset_id::text, d.amount_atomic::text, d.status, d.risk_status FROM deposits d WHERE d.id = $1 FOR UPDATE`, depositID).Scan(&userID, &assetID, &amount, &status, &riskStatus)
	if errors.Is(err, pgx.ErrNoRows) || status != "pending_risk_review" || (riskStatus != "not_started" && riskStatus != "pending") {
		return TransferResult{}, ErrDepositNotCreditable
	}
	if err != nil {
		return TransferResult{}, err
	}
	var kycApproved bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM kyc_profiles WHERE user_id = $1 AND status = 'approved')`, userID).Scan(&kycApproved); err != nil || !kycApproved {
		if err != nil {
			return TransferResult{}, err
		}
		return TransferResult{}, ErrDepositNotCreditable
	}
	var destinationAccountID, symbol, network string
	err = tx.QueryRow(ctx, `SELECT a.id::text, asset.symbol, asset.network FROM accounts a JOIN assets asset ON asset.id = $2 WHERE a.user_id = $1 AND a.kind = 'funding' AND a.status = 'active'`, userID, assetID).Scan(&destinationAccountID, &symbol, &network)
	if err != nil {
		return TransferResult{}, ErrDepositNotCreditable
	}
	systemName := "custody-clearing-" + assetID
	var systemAccountID string
	err = tx.QueryRow(ctx, `INSERT INTO accounts (user_id, kind, name) VALUES (NULL, 'system', $1) ON CONFLICT (name) WHERE kind = 'system' DO UPDATE SET name = EXCLUDED.name RETURNING id::text`, systemName).Scan(&systemAccountID)
	if err != nil {
		return TransferResult{}, err
	}
	result := TransferResult{}
	err = tx.QueryRow(ctx, `INSERT INTO journals (idempotency_key, reference_type, reference_id) VALUES ($1, 'deposit_credit', $2) RETURNING id::text`, "deposit-credit-"+depositID, depositID).Scan(&result.JournalID)
	if err != nil {
		return TransferResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO postings (journal_id, account_id, asset_id, bucket, direction, amount_atomic) VALUES ($1, $2, $3, 'available', 'debit', $4), ($1, $5, $3, 'available', 'credit', $4)`, result.JournalID, systemAccountID, assetID, amount, destinationAccountID); err != nil {
		return TransferResult{}, err
	}
	command, err := tx.Exec(ctx, `UPDATE deposits SET status = 'credited', risk_status = 'approved', credited_at = now(), updated_at = now() WHERE id = $1 AND status = 'pending_risk_review'`, depositID)
	if err != nil || command.RowsAffected() != 1 {
		if err != nil {
			return TransferResult{}, err
		}
		return TransferResult{}, ErrDepositNotCreditable
	}
	metadata, err := json.Marshal(map[string]string{"reason": reason, "asset_symbol": symbol, "network": network, "amount_atomic": amount})
	if err != nil {
		return TransferResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events (actor_id, actor_type, action, resource_type, resource_id, reason, metadata) VALUES ($1, 'user', 'deposit.credited', 'deposit', $2, $3, $4::jsonb)`, reviewerID, depositID, reason, metadata); err != nil {
		return TransferResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events (event_type, aggregate_type, aggregate_id, payload) VALUES ('deposit.credited', 'deposit', $1, $2::jsonb)`, depositID, metadata); err != nil {
		return TransferResult{}, err
	}
	result.TransferID = depositID
	return result, tx.Commit(ctx)
}

type WithdrawalInput struct {
	UserID          string
	SourceAccountID string
	AssetSymbol     string
	Network         string
	Address         string
	Tag             string
	AmountAtomic    int64
	IdempotencyKey  string
}

type WithdrawalResult struct {
	WithdrawalID string
	JournalID    string
	Duplicate    bool
	Status       string
}

type WithdrawalCancellationResult struct {
	WithdrawalID string
	JournalID    string
	Status       string
}

var ErrWithdrawalNotCancellable = errors.New("withdrawal is not cancellable")

// RequestWithdrawal holds funds by moving them from available to held inside
// an immutable journal. It is intentionally not a custody broadcast command.
func (m *Manager) RequestWithdrawal(ctx context.Context, input WithdrawalInput) (WithdrawalResult, error) {
	if input.AmountAtomic <= 0 || input.IdempotencyKey == "" {
		return WithdrawalResult{}, ErrWithdrawalNotAllowed
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return WithdrawalResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var withdrawalsEnabled bool
	if err = tx.QueryRow(ctx, `SELECT enabled FROM operational_controls WHERE control_key='withdrawals_enabled' FOR SHARE`).Scan(&withdrawalsEnabled); err != nil {
		return WithdrawalResult{}, err
	}
	if !withdrawalsEnabled {
		return WithdrawalResult{}, ErrWithdrawalsDisabled
	}
	var result WithdrawalResult
	err = tx.QueryRow(ctx, `SELECT id::text, (SELECT id::text FROM journals WHERE withdrawal_id = withdrawals.id), status FROM withdrawals WHERE idempotency_key=$1`, input.IdempotencyKey).Scan(&result.WithdrawalID, &result.JournalID, &result.Status)
	if err == nil {
		result.Duplicate = true
		return result, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return WithdrawalResult{}, err
	}
	// Address activation is time-based policy state. Promote elapsed cooldowns
	// in the same transaction so a customer does not need to visit the address
	// book before a legitimate withdrawal can proceed.
	if _, err = tx.Exec(ctx, `UPDATE withdrawal_addresses SET status='active' WHERE user_id=$1 AND status='pending' AND activated_at <= now()`, input.UserID); err != nil {
		return WithdrawalResult{}, err
	}
	var assetID, addressID string
	err = tx.QueryRow(ctx, `SELECT a.id::text, w.id::text FROM assets a JOIN withdrawal_addresses w ON w.asset_id=a.id WHERE a.symbol=$1 AND a.network=$2 AND a.status='enabled' AND w.user_id=$3 AND w.address=$4 AND w.tag=$5 AND w.status='active' AND w.activated_at <= now()`, input.AssetSymbol, input.Network, input.UserID, input.Address, input.Tag).Scan(&assetID, &addressID)
	if errors.Is(err, pgx.ErrNoRows) {
		return WithdrawalResult{}, ErrWithdrawalNotAllowed
	}
	if err != nil {
		return WithdrawalResult{}, err
	}
	var kycOK bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM kyc_profiles WHERE user_id=$1 AND status='approved')`, input.UserID).Scan(&kycOK); err != nil || !kycOK {
		if err != nil {
			return WithdrawalResult{}, err
		}
		return WithdrawalResult{}, ErrWithdrawalNotAllowed
	}
	var owner, accountStatus string
	if err = tx.QueryRow(ctx, `SELECT user_id::text,status FROM accounts WHERE id=$1 FOR UPDATE`, input.SourceAccountID).Scan(&owner, &accountStatus); err != nil || owner != input.UserID || accountStatus != "active" {
		return WithdrawalResult{}, ErrWithdrawalNotAllowed
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, input.SourceAccountID+":"+assetID); err != nil {
		return WithdrawalResult{}, err
	}
	var available int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_atomic ELSE -amount_atomic END),0)::bigint FROM postings WHERE account_id=$1 AND asset_id=$2 AND bucket='available'`, input.SourceAccountID, assetID).Scan(&available); err != nil {
		return WithdrawalResult{}, err
	}
	if available < input.AmountAtomic {
		return WithdrawalResult{}, ErrWithdrawalNotAllowed
	}
	err = tx.QueryRow(ctx, `INSERT INTO withdrawals (user_id,account_id,asset_id,withdrawal_address_id,amount_atomic,idempotency_key,status) VALUES ($1,$2,$3,$4,$5,$6,'pending_approval') RETURNING id::text,status`, input.UserID, input.SourceAccountID, assetID, addressID, input.AmountAtomic, input.IdempotencyKey).Scan(&result.WithdrawalID, &result.Status)
	if err != nil {
		return WithdrawalResult{}, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO journals (idempotency_key,reference_type,reference_id,withdrawal_id) VALUES ($1,'withdrawal_hold',$2,$2::uuid) RETURNING id::text`, "withdrawal-hold-"+result.WithdrawalID, result.WithdrawalID).Scan(&result.JournalID)
	if err != nil {
		return WithdrawalResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO postings (journal_id,account_id,asset_id,bucket,direction,amount_atomic) VALUES ($1,$2,$3,'available','debit',$4),($1,$2,$3,'held','credit',$4)`, result.JournalID, input.SourceAccountID, assetID, input.AmountAtomic); err != nil {
		return WithdrawalResult{}, err
	}
	payload, err := json.Marshal(map[string]any{"withdrawal_id": result.WithdrawalID, "asset_symbol": input.AssetSymbol, "network": input.Network, "amount_atomic": input.AmountAtomic})
	if err != nil {
		return WithdrawalResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events (actor_id,actor_type,action,resource_type,resource_id,metadata) VALUES ($1,'user','withdrawal.requested','withdrawal',$2,$3::jsonb)`, input.UserID, result.WithdrawalID, payload); err != nil {
		return WithdrawalResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events (event_type,aggregate_type,aggregate_id,payload) VALUES ('withdrawal.submitted','withdrawal',$1,$2::jsonb)`, result.WithdrawalID, payload); err != nil {
		return WithdrawalResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events (event_type,aggregate_type,aggregate_id,payload) VALUES ('withdrawal.under_review','withdrawal',$1,$2::jsonb)`, result.WithdrawalID, payload); err != nil {
		return WithdrawalResult{}, err
	}
	return result, tx.Commit(ctx)
}

// CancelWithdrawal releases a customer's held balance only while a withdrawal
// has not been approved or submitted. The release is a new immutable journal;
// the original hold is never edited or deleted.
func (m *Manager) CancelWithdrawal(ctx context.Context, userID, withdrawalID string) (WithdrawalCancellationResult, error) {
	if userID == "" || withdrawalID == "" {
		return WithdrawalCancellationResult{}, ErrWithdrawalNotCancellable
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return WithdrawalCancellationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var accountID, assetID, status string
	var amount int64
	err = tx.QueryRow(ctx, `SELECT account_id::text,asset_id::text,amount_atomic::bigint,status FROM withdrawals WHERE id=$1 AND user_id=$2 FOR UPDATE`, withdrawalID, userID).Scan(&accountID, &assetID, &amount, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return WithdrawalCancellationResult{}, ErrWithdrawalNotCancellable
	}
	if err != nil {
		return WithdrawalCancellationResult{}, err
	}
	if status != "pending_approval" {
		return WithdrawalCancellationResult{}, ErrWithdrawalNotCancellable
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, accountID+":"+assetID); err != nil {
		return WithdrawalCancellationResult{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE withdrawals SET status='cancelled',updated_at=now() WHERE id=$1 AND status='pending_approval'`, withdrawalID); err != nil {
		return WithdrawalCancellationResult{}, err
	}
	var result WithdrawalCancellationResult
	result.WithdrawalID, result.Status = withdrawalID, "cancelled"
	err = tx.QueryRow(ctx, `INSERT INTO journals (idempotency_key,reference_type,reference_id,withdrawal_id) VALUES ($1,'withdrawal_cancel',$2,$2::uuid) RETURNING id::text`, "withdrawal-cancel-"+withdrawalID, withdrawalID).Scan(&result.JournalID)
	if err != nil {
		return WithdrawalCancellationResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO postings (journal_id,account_id,asset_id,bucket,direction,amount_atomic) VALUES ($1,$2,$3,'held','debit',$4),($1,$2,$3,'available','credit',$4)`, result.JournalID, accountID, assetID, amount); err != nil {
		return WithdrawalCancellationResult{}, err
	}
	payload, err := json.Marshal(map[string]any{"withdrawal_id": withdrawalID, "amount_atomic": amount})
	if err != nil {
		return WithdrawalCancellationResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events (actor_id,actor_type,action,resource_type,resource_id,metadata) VALUES ($1,'user','withdrawal.cancelled','withdrawal',$2,$3::jsonb)`, userID, withdrawalID, payload); err != nil {
		return WithdrawalCancellationResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events (event_type,aggregate_type,aggregate_id,payload) VALUES ('withdrawal.cancelled','withdrawal',$1,$2::jsonb)`, withdrawalID, payload); err != nil {
		return WithdrawalCancellationResult{}, err
	}
	return result, tx.Commit(ctx)
}

func (m *Manager) SaveWithdrawalTravelRule(ctx context.Context, userID, withdrawalID, ciphertext string) error {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM withdrawals WHERE id=$1 AND user_id=$2 FOR UPDATE`, withdrawalID, userID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrWithdrawalNotCancellable
	}
	if err != nil {
		return err
	}
	if status != "pending_approval" {
		return ErrWithdrawalNotCancellable
	}
	_, err = tx.Exec(ctx, `INSERT INTO withdrawal_travel_rules (withdrawal_id,ciphertext) VALUES ($1,$2)`, withdrawalID, ciphertext)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events (actor_id,actor_type,action,resource_type,resource_id) VALUES ($1,'user','withdrawal.travel_rule_captured','withdrawal',$2)`, userID, withdrawalID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type CatalogAsset struct {
	Symbol      string
	DisplayName string
	Status      string
}

type CatalogNetwork struct {
	Code        string
	DisplayName string
	Status      string
}
type AccountsRepository interface {
	CreateUser(context.Context, string, string, string) (User, error)
	CreateDefaultAccounts(context.Context, string) (fundingID, utaID string, err error)
	SetVerifiedPhone(context.Context, string, string) error
}
type VerificationRepository interface {
	CreateEmailChallenge(context.Context, string, []byte, time.Time) error
	VerifyEmailChallenge(context.Context, string, []byte) (userID string, verified bool, err error)
}
type KYCRepository interface {
	Transition(context.Context, string, string, string, int16, string, string) error
	Start(context.Context, string, string, string) error
}
type SessionsRepository interface {
	Create(context.Context, string, []byte, time.Time, SessionMetadata) (string, error)
	Revoke(context.Context, string) error
}
type AuditRepository interface {
	Record(context.Context, string, string, string, string, any) error
}
type OutboxRepository interface {
	Enqueue(context.Context, string, string, string, any) error
}

type accountRepository struct{ tx pgx.Tx }

func (r accountRepository) CreateUser(ctx context.Context, email, passwordHash, countryCode string) (User, error) {
	var user User
	err := r.tx.QueryRow(ctx, `INSERT INTO users (email, password_hash, country_code) VALUES ($1, $2, $3) RETURNING id::text, uid, email, status`, email, passwordHash, countryCode).Scan(&user.ID, &user.UID, &user.Email, &user.Status)
	return user, err
}
func (r accountRepository) CreateDefaultAccounts(ctx context.Context, userID string) (string, string, error) {
	var funding, uta string
	if err := r.tx.QueryRow(ctx, `INSERT INTO accounts (user_id, kind, name) VALUES ($1, 'funding', 'Funding Account') RETURNING id::text`, userID).Scan(&funding); err != nil {
		return "", "", err
	}
	if err := r.tx.QueryRow(ctx, `INSERT INTO accounts (user_id, kind, name) VALUES ($1, 'uta', 'Unified Trading Account') RETURNING id::text`, userID).Scan(&uta); err != nil {
		return "", "", err
	}
	return funding, uta, nil
}

func (r accountRepository) SetVerifiedPhone(ctx context.Context, userID, phone string) error {
	_, err := r.tx.Exec(ctx, `UPDATE users SET phone_e164 = $2, phone_verified_at = now(), updated_at = now() WHERE id = $1`, userID, phone)
	return err
}

type auditRepository struct{ tx pgx.Tx }

type verificationRepository struct{ tx pgx.Tx }

func (r verificationRepository) CreateEmailChallenge(ctx context.Context, userID string, codeHash []byte, expiresAt time.Time) error {
	if _, err := r.tx.Exec(ctx, `UPDATE email_verification_challenges SET consumed_at = now() WHERE user_id = $1 AND consumed_at IS NULL`, userID); err != nil {
		return err
	}
	_, err := r.tx.Exec(ctx, `INSERT INTO email_verification_challenges (user_id, code_hash, expires_at) VALUES ($1, $2, $3)`, userID, codeHash, expiresAt)
	return err
}

func (r verificationRepository) VerifyEmailChallenge(ctx context.Context, email string, suppliedHash []byte) (string, bool, error) {
	var challengeID, userID string
	var storedHash []byte
	var expiresAt time.Time
	var attempts int16
	err := r.tx.QueryRow(ctx, `SELECT ev.id::text, ev.user_id::text, ev.code_hash, ev.expires_at, ev.attempts FROM email_verification_challenges ev JOIN users u ON u.id = ev.user_id WHERE u.email = $1 AND ev.consumed_at IS NULL ORDER BY ev.created_at DESC LIMIT 1 FOR UPDATE`, email).Scan(&challengeID, &userID, &storedHash, &expiresAt, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if expiresAt.Before(time.Now()) || attempts >= 5 || subtle.ConstantTimeCompare(storedHash, suppliedHash) != 1 {
		_, err = r.tx.Exec(ctx, `UPDATE email_verification_challenges SET attempts = LEAST(attempts + 1, 5) WHERE id = $1`, challengeID)
		return "", false, err
	}
	if _, err := r.tx.Exec(ctx, `UPDATE email_verification_challenges SET consumed_at = now() WHERE id = $1`, challengeID); err != nil {
		return "", false, err
	}
	if _, err := r.tx.Exec(ctx, `UPDATE users SET status = 'active', updated_at = now() WHERE id = $1 AND status = 'pending_verification'`, userID); err != nil {
		return "", false, err
	}
	return userID, true, nil
}

type sessionRepository struct{ tx pgx.Tx }

func (r sessionRepository) Create(ctx context.Context, userID string, tokenHash []byte, expiresAt time.Time, meta SessionMetadata) (string, error) {
	var id string
	err := r.tx.QueryRow(ctx, `INSERT INTO sessions (user_id, token_hash, expires_at, user_agent, client_ip) VALUES ($1, $2, $3, $4, NULLIF($5,'')::inet) RETURNING id::text`, userID, tokenHash, expiresAt, meta.UserAgent, meta.ClientIP).Scan(&id)
	return id, err
}

func (r sessionRepository) Revoke(ctx context.Context, sessionID string) error {
	command, err := r.tx.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, sessionID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errors.New("session is not active")
	}
	return nil
}

type kycRepository struct{ tx pgx.Tx }

func (r kycRepository) Start(ctx context.Context, userID, applicantID, levelName string) error {
	_, err := r.tx.Exec(ctx, `INSERT INTO kyc_profiles (user_id, provider, applicant_id, level_name, status) VALUES ($1, 'sumsub', $2, $3, 'pending') ON CONFLICT (user_id) DO UPDATE SET applicant_id = EXCLUDED.applicant_id, level_name = EXCLUDED.level_name, status = 'pending', updated_at = now() WHERE kyc_profiles.status IN ('not_started', 'pending', 'on_hold')`, userID, applicantID, levelName)
	return err
}

func (r kycRepository) Transition(ctx context.Context, userID, from, to string, tier int16, provider, applicantID string) error {
	command, err := r.tx.Exec(ctx, `INSERT INTO kyc_profiles (user_id, provider, applicant_id, tier, status) VALUES ($1, $2, NULLIF($3, ''), $4, $5::kyc_status) ON CONFLICT (user_id) DO UPDATE SET provider=EXCLUDED.provider, applicant_id=COALESCE(EXCLUDED.applicant_id, kyc_profiles.applicant_id), tier=EXCLUDED.tier, status=EXCLUDED.status, reviewed_at=CASE WHEN $5 IN ('approved','rejected') THEN now() ELSE kyc_profiles.reviewed_at END, updated_at=now() WHERE kyc_profiles.status=$6::kyc_status`, userID, provider, applicantID, tier, to, from)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errors.New("kyc profile status changed concurrently")
	}
	return nil
}

func (r auditRepository) Record(ctx context.Context, actorType, action, resourceType, resourceID string, metadata any) error {
	payload, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = r.tx.Exec(ctx, `INSERT INTO audit_events (actor_type, action, resource_type, resource_id, metadata) VALUES ($1, $2, $3, $4, $5::jsonb)`, actorType, action, resourceType, resourceID, payload)
	return err
}

type outboxRepository struct{ tx pgx.Tx }

func (r outboxRepository) Enqueue(ctx context.Context, eventType, aggregateType, aggregateID string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = r.tx.Exec(ctx, `INSERT INTO outbox_events (event_type, aggregate_type, aggregate_id, payload) VALUES ($1, $2, $3, $4::jsonb)`, eventType, aggregateType, aggregateID, body)
	return err
}

package httpserver

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/limiance/backend/internal/auth"
	"github.com/limiance/backend/internal/datamanager"
)

const sessionCookieName = "limiance_session"

type AuthHandler struct {
	service      *auth.Service
	verification *auth.EmailVerificationService
	logger       *slog.Logger
	secureCookie bool
	sameSite     http.SameSite
}

type EmailVerificationHandler struct {
	service *auth.EmailVerificationService
	logger  *slog.Logger
}

type ResendEmailInput struct {
	Email string `json:"email"`
}

func NewEmailVerificationHandler(service *auth.EmailVerificationService, logger *slog.Logger) *EmailVerificationHandler {
	return &EmailVerificationHandler{service: service, logger: logger}
}

func (h *EmailVerificationHandler) Verify(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var input auth.VerifyEmailInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if err := h.service.Verify(r.Context(), input); err != nil {
		if errors.Is(err, auth.ErrInvalidVerification) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_or_expired_code"})
			return
		}
		h.logger.Error("email verification failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "verification_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "email_verified"})
}

func (h *EmailVerificationHandler) Resend(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var input ResendEmailInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if err := h.service.Resend(r.Context(), input.Email); err != nil {
		h.logger.Error("email verification resend failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "verification_unavailable"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "if_required_sent"})
}

func NewAuthHandler(service *auth.Service, logger *slog.Logger, secureCookie bool) *AuthHandler {
	sameSite := http.SameSiteStrictMode
	if secureCookie {
		// The deployed Vercel frontends are cross-site relative to the API.
		// SameSite=None is necessary for fetch(..., {credentials: "include"}).
		sameSite = http.SameSiteNoneMode
	}
	return &AuthHandler{service: service, logger: logger, secureCookie: secureCookie, sameSite: sameSite}
}

// WithEmailVerification configures the authenticated-login path to issue a
// fresh verification challenge after valid credentials are supplied for an
// inactive account. This is deliberately not performed for invalid logins, so
// it does not create an account-enumeration or email-spam primitive.
func (h *AuthHandler) WithEmailVerification(service *auth.EmailVerificationService) *AuthHandler {
	h.verification = service
	return h
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var input auth.LoginInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	input.UserAgent, input.ClientIP = requestDeviceMetadata(r)
	result, err := h.service.Login(r.Context(), input)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrMFARequired):
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "mfa_required", "mfa_token": result.MFAToken})
			return
		case errors.Is(err, auth.ErrInvalidCredentials):
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_credentials"})
		case errors.Is(err, auth.ErrAccountFrozen):
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "account_frozen"})
		case errors.Is(err, auth.ErrVerificationNeeded):
			resent := false
			if h.verification != nil {
				if resendErr := h.verification.Resend(r.Context(), input.Email); resendErr != nil {
					h.logger.Error("verification resend after login failed", "error", resendErr)
				} else {
					resent = true
				}
			}
			// The frontend must handle this response by routing to its verification
			// page. HTTP APIs do not redirect an XHR/fetch request into a UI route.
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error":                    "email_verification_required",
				"verification_code_resent": resent,
			})
		default:
			h.logger.Error("login failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "login_failed"})
		}
		return
	}
	h.setSessionCookie(w, result)
	writeJSON(w, http.StatusOK, map[string]string{"status": "authenticated"})
}

func (h *AuthHandler) SubaccountLogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var input auth.SubaccountLoginInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	input.UserAgent, input.ClientIP = requestDeviceMetadata(r)
	result, err := h.service.SubaccountLogin(r.Context(), input)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_credentials"})
			return
		}
		if errors.Is(err, auth.ErrAccountFrozen) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "subaccount_unavailable"})
			return
		}
		h.logger.Error("subaccount login failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "login_failed"})
		return
	}
	h.setSessionCookie(w, result)
	writeJSON(w, http.StatusOK, map[string]string{"status": "authenticated", "principal_type": "subaccount"})
}

func (h *AuthHandler) VerifyTOTPLogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	defer r.Body.Close()
	var input struct {
		MFAToken string `json:"mfa_token"`
		Code     string `json:"code"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	result, err := h.service.VerifyTOTPLogin(r.Context(), input.MFAToken, input.Code)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidTOTP) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_authenticator_code"})
			return
		}
		if errors.Is(err, auth.ErrTOTPUnavailable) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "totp_unavailable"})
			return
		}
		h.logger.Error("totp login verification failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication_unavailable"})
		return
	}
	h.setSessionCookie(w, result)
	writeJSON(w, http.StatusOK, map[string]string{"status": "authenticated"})
}

func (h *AuthHandler) PasswordResetRequest(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	defer r.Body.Close()
	var input struct {
		Email string `json:"email"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if err := h.service.RequestPasswordReset(r.Context(), input.Email); err != nil {
		h.logger.Error("password reset request failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "password_reset_unavailable"})
		return
	}
	// Deliberately identical for known and unknown email addresses.
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "if_required_sent"})
}

func (h *AuthHandler) PasswordResetConfirm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var input struct {
		Email       string `json:"email"`
		Code        string `json:"code"`
		NewPassword string `json:"new_password"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if err := h.service.ResetPassword(r.Context(), input.Email, input.Code, input.NewPassword); err != nil {
		if errors.Is(err, auth.ErrInvalidReset) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_or_expired_code"})
			return
		}
		h.logger.Error("password reset failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "password_reset_unavailable"})
		return
	}
	h.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "password_reset"})
}

func (h *AuthHandler) StepUp(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	defer r.Body.Close()
	var input struct {
		Purpose string `json:"purpose"`
		Code    string `json:"code"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	token, expiresAt, err := h.service.StepUp(r.Context(), p, input.Purpose, input.Code)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidTOTP):
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_authenticator_code"})
		case errors.Is(err, auth.ErrTOTPUnavailable):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "totp_not_enabled"})
		case errors.Is(err, auth.ErrInvalidCredentials):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_step_up_request"})
		default:
			h.logger.Error("step-up verification failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "step_up_unavailable"})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"step_up_token": token, "expires_at": expiresAt})
}

func (h *AuthHandler) MFARecoveryRequest(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var input struct {
		Email  string `json:"email"`
		Reason string `json:"reason"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if err := h.service.RequestMFARecovery(r.Context(), input.Email, input.Reason); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_recovery_request"})
			return
		}
		h.logger.Error("mfa recovery request failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "mfa_recovery_unavailable"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "if_required_submitted"})
}

func (h *AuthHandler) TOTPEnroll(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	secret, uri, err := h.service.EnrollTOTP(r.Context(), p.UserID, "Limiance", p.Email)
	if err != nil {
		if errors.Is(err, auth.ErrTOTPUnavailable) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "totp_unavailable"})
			return
		}
		h.logger.Error("totp enrollment failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "totp_enrollment_unavailable"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"secret": secret, "otpauth_uri": uri})
}

func (h *AuthHandler) TOTPConfirm(w http.ResponseWriter, r *http.Request) { h.updateTOTP(w, r, true) }
func (h *AuthHandler) TOTPDisable(w http.ResponseWriter, r *http.Request) { h.updateTOTP(w, r, false) }
func (h *AuthHandler) updateTOTP(w http.ResponseWriter, r *http.Request, enable bool) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	defer r.Body.Close()
	var input struct {
		Code string `json:"code"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	var err error
	if enable {
		err = h.service.ConfirmTOTP(r.Context(), p.UserID, input.Code)
	} else {
		err = h.service.DisableTOTP(r.Context(), p.UserID, input.Code)
	}
	if err != nil {
		if errors.Is(err, auth.ErrInvalidTOTP) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_authenticator_code"})
			return
		}
		if errors.Is(err, auth.ErrTOTPUnavailable) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "totp_unavailable"})
			return
		}
		h.logger.Error("totp update failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "totp_update_unavailable"})
		return
	}
	status := "totp_disabled"
	if enable {
		status = "totp_enabled"
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

func (h *AuthHandler) setSessionCookie(w http.ResponseWriter, result auth.LoginResult) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: result.Token, Path: "/", Expires: result.ExpiresAt, MaxAge: int(time.Until(result.ExpiresAt).Seconds()), HttpOnly: true, Secure: h.secureCookie, SameSite: h.sameSite})
}

func (h *AuthHandler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: h.secureCookie, SameSite: h.sameSite})
}

func (h *AuthHandler) Session(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":             principal.UserID,
		"uid":                 principal.UID,
		"email":               principal.Email,
		"active_account_id":   principal.ActiveAccountID,
		"active_account_kind": principal.ActiveAccountKind,
	})
}

func (h *AuthHandler) Sessions(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	items, err := h.service.ActiveSessions(r.Context(), p)
	if err != nil {
		h.logger.Error("active sessions read failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sessions_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": items})
}

func (h *AuthHandler) RevokeSession(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	id := r.PathValue("session_id")
	changed, err := h.service.RevokeSession(r.Context(), p, id)
	if err != nil {
		h.logger.Error("session revocation failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "session_revocation_unavailable"})
		return
	}
	if !changed {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session_not_found"})
		return
	}
	if id == p.SessionID {
		h.clearSessionCookie(w)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked", "current": id == p.SessionID})
}

func (h *AuthHandler) RevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	count, err := h.service.RevokeOtherSessions(r.Context(), p)
	if err != nil {
		h.logger.Error("other session revocation failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "session_revocation_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked", "count": count})
}

func (h *AuthHandler) SetAntiPhishingCode(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	defer r.Body.Close()
	var input struct {
		Code string `json:"code"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if err := h.service.SetAntiPhishingCode(r.Context(), p, input.Code); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_anti_phishing_code"})
			return
		}
		h.logger.Error("anti-phishing code update failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "security_settings_unavailable"})
		return
	}
	status := "anti_phishing_code_set"
	if input.Code == "" {
		status = "anti_phishing_code_cleared"
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

func (h *AuthHandler) AntiPhishingStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	status, err := h.service.AntiPhishingStatus(r.Context(), p)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "security_settings_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *AuthHandler) GeneratedAntiPhishingCode(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	if r.Method == http.MethodDelete {
		if !h.authorizeStepUp(w, r, p, "anti_phishing_code_disabled") {
			return
		}
		if err := h.service.DisableAntiPhishingCode(r.Context(), p); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "security_settings_unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "disabled", "message": "Anti-phishing code disabled."})
		return
	}
	if r.Method == http.MethodPost {
		status, err := h.service.AntiPhishingStatus(r.Context(), p)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "security_settings_unavailable"})
			return
		}
		if status.Enabled && !h.authorizeStepUp(w, r, p, "anti_phishing_code_regenerated") {
			return
		}
	}
	if err := h.service.EnableAntiPhishingCode(r.Context(), p); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "security_settings_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "enabled", "message": "Your anti-phishing code was generated and sent by email."})
}

func (h *AuthHandler) authorizeStepUp(w http.ResponseWriter, r *http.Request, p auth.Principal, purpose string) bool {
	valid, err := h.service.ConsumeStepUp(r.Context(), p, purpose, r.Header.Get("X-Step-Up-Token"))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "step_up_unavailable"})
		return false
	}
	if !valid {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_step_up_token"})
		return false
	}
	return true
}

func requestDeviceMetadata(r *http.Request) (string, string) {
	userAgent := r.UserAgent()
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = ""
	}
	return userAgent, host
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	if err := h.service.Logout(r.Context(), principal); err != nil {
		h.logger.Error("logout failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "logout_failed"})
		return
	}
	h.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

// Freeze signs the customer out everywhere immediately. It intentionally has
// no unfreeze counterpart in the public API: that requires a separately
// authenticated and audited support process.
func (h *AuthHandler) Freeze(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	if err := h.service.Freeze(r.Context(), principal); err != nil {
		if errors.Is(err, datamanager.ErrUserNotFreezable) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "account_not_freezable"})
			return
		}
		h.logger.Error("account freeze failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "account_freeze_unavailable"})
		return
	}
	h.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "account_frozen"})
}

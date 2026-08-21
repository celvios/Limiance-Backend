package httpserver

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/limiance/backend/internal/admin"
	"github.com/limiance/backend/internal/datamanager"
)

type AdminHandler struct {
	service *admin.Service
	logger  *slog.Logger
}

func NewAdminHandler(service *admin.Service, logger *slog.Logger) *AdminHandler {
	return &AdminHandler{service: service, logger: logger}
}

func (h *AdminHandler) ApproveDeposit(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	defer r.Body.Close()
	var input admin.DepositApproval
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	result, err := h.service.ApproveDeposit(r.Context(), principal.UserID, r.PathValue("deposit_id"), input)
	if err != nil {
		switch {
		case errors.Is(err, admin.ErrInvalidApproval):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_approval"})
		case errors.Is(err, datamanager.ErrDepositNotCreditable):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "deposit_not_creditable"})
		default:
			h.logger.Error("deposit approval failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "deposit_approval_unavailable"})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deposit_id": result.TransferID, "journal_id": result.JournalID, "status": "credited"})
}

func (h *AdminHandler) ApproveWithdrawal(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	defer r.Body.Close()
	var input admin.DepositApproval
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	result, err := h.service.ApproveWithdrawal(r.Context(), p.UserID, r.PathValue("withdrawal_id"), input)
	if err != nil {
		if errors.Is(err, admin.ErrInvalidWithdrawalApproval) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_approval"})
		} else if errors.Is(err, datamanager.ErrWithdrawalsDisabled) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "withdrawals_temporarily_disabled"})
		} else if errors.Is(err, datamanager.ErrWithdrawalNotApprovable) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "withdrawal_not_approvable"})
		} else {
			h.logger.Error("withdrawal approval failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "withdrawal_approval_unavailable"})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"withdrawal_id": result.WithdrawalID, "approval_count": result.ApprovalCount, "status": result.Status})
}

func (h *AdminHandler) WithdrawalControl(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	if r.Method == http.MethodGet {
		enabled, err := h.service.WithdrawalsEnabledForAdministrator(r.Context(), p.UserID)
		if err != nil {
			if errors.Is(err, admin.ErrNotPlatformAdministrator) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "administrator_role_required"})
				return
			}
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "withdrawal_control_unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"withdrawals_enabled": enabled})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	defer r.Body.Close()
	var input struct {
		Enabled bool `json:"enabled"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if err := h.service.SetWithdrawalsEnabled(r.Context(), p.UserID, input.Enabled); err != nil {
		if errors.Is(err, admin.ErrNotPlatformAdministrator) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "administrator_role_required"})
			return
		}
		h.logger.Error("withdrawal control change failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "withdrawal_control_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"withdrawals_enabled": input.Enabled})
}

func (h *AdminHandler) UserRoles(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	userID := r.PathValue("user_id")
	if r.Method == http.MethodGet {
		roles, err := h.service.UserRoles(r.Context(), p.UserID, userID)
		if errors.Is(err, admin.ErrNotPlatformAdministrator) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "administrator_role_required"})
			return
		}
		if err != nil {
			h.logger.Error("admin role list failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "role_management_unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"roles": roles})
		return
	}
	role := r.PathValue("role")
	grant := r.Method == http.MethodPut
	changed, err := h.service.SetUserRole(r.Context(), p.UserID, userID, role, grant)
	if errors.Is(err, admin.ErrNotPlatformAdministrator) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "administrator_role_required"})
		return
	}
	if errors.Is(err, admin.ErrInvalidRole) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_role"})
		return
	}
	if errors.Is(err, datamanager.ErrLastAdministrator) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "final_platform_administrator"})
		return
	}
	if err != nil {
		h.logger.Error("admin role update failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "role_management_unavailable"})
		return
	}
	if !changed {
		writeJSON(w, http.StatusOK, map[string]string{"status": "unchanged"})
		return
	}
	status := "granted"
	if !grant {
		status = "revoked"
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

func (h *AdminHandler) ConversionPair(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	defer r.Body.Close()
	var input datamanager.ConversionPairPolicyInput
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if err := h.service.SetConversionPair(r.Context(), p.UserID, input); err != nil {
		if errors.Is(err, admin.ErrInvalidRole) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_conversion_pair"})
		} else if errors.Is(err, datamanager.ErrAdministratorRoleRequired) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "administrator_role_required"})
		} else if errors.Is(err, datamanager.ErrConversionPairUnavailable) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "conversion_pair_unavailable"})
		} else {
			h.logger.Error("conversion pair update failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "conversion_pair_unavailable"})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "configured"})
}

func (h *AdminHandler) ConversionControl(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	if r.Method == http.MethodGet {
		enabled, err := h.service.ConversionsEnabledForAdministrator(r.Context(), p.UserID)
		if errors.Is(err, admin.ErrNotPlatformAdministrator) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "administrator_role_required"})
		} else if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "conversion_control_unavailable"})
		} else {
			writeJSON(w, http.StatusOK, map[string]bool{"conversions_enabled": enabled})
		}
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	defer r.Body.Close()
	var input struct {
		Enabled bool `json:"enabled"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if err := h.service.SetConversionsEnabled(r.Context(), p.UserID, input.Enabled); err != nil {
		if errors.Is(err, datamanager.ErrAdministratorRoleRequired) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "administrator_role_required"})
		} else {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "conversion_control_unavailable"})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"conversions_enabled": input.Enabled})
}

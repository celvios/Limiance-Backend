package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/limiance/backend/internal/accounts"
	"github.com/limiance/backend/internal/datamanager"
)

type ProfileHandler struct {
	data     *datamanager.Manager
	accounts *accounts.Service
}

func NewProfileHandler(data *datamanager.Manager, accounts *accounts.Service) *ProfileHandler {
	return &ProfileHandler{data: data, accounts: accounts}
}
func (h *ProfileHandler) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	profile, err := h.data.UserProfile(r.Context(), p.UserID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "profile_unavailable"})
		return
	}
	balances, err := h.accounts.Balances(r.Context(), p.UserID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "balances_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": profile, "balances": balances})
}
func (h *ProfileHandler) Preferences(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	var in struct {
		DisplayName       string `json:"display_name"`
		PreferredCurrency string `json:"preferred_currency"`
		PreferredLanguage string `json:"preferred_language"`
		PreferredTheme    string `json:"preferred_theme"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.PreferredCurrency = strings.ToUpper(strings.TrimSpace(in.PreferredCurrency))
	in.PreferredLanguage = strings.ToLower(strings.TrimSpace(in.PreferredLanguage))
	in.PreferredTheme = strings.ToLower(strings.TrimSpace(in.PreferredTheme))
	if len(in.DisplayName) > 80 || len(in.PreferredCurrency) != 3 || len(in.PreferredLanguage) < 2 || len(in.PreferredLanguage) > 12 || (in.PreferredTheme != "system" && in.PreferredTheme != "light" && in.PreferredTheme != "dark") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_preferences"})
		return
	}
	profile, err := h.data.UpdateUserPreferences(r.Context(), p.UserID, in.DisplayName, in.PreferredCurrency, in.PreferredLanguage, in.PreferredTheme)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "preferences_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": profile})
}

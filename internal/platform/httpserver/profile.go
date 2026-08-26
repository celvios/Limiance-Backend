package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
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
	preferences, err := h.data.UserPreferences(r.Context(), p.UserID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "preferences_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": profile, "preferences": preferences, "balances": balances})
}
func (h *ProfileHandler) Preferences(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	var in struct {
		DisplayName           string `json:"display_name"`
		PreferredCurrency     string `json:"preferred_currency"`
		SecondaryDisplayAsset string `json:"secondary_display_asset"`
		PreferredLanguage     string `json:"preferred_language"`
		PreferredTheme        string `json:"preferred_theme"`
		datamanager.UserPreferences
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.PreferredCurrency = strings.ToUpper(strings.TrimSpace(in.PreferredCurrency))
	in.SecondaryDisplayAsset = strings.ToUpper(strings.TrimSpace(in.SecondaryDisplayAsset))
	in.PreferredLanguage = strings.ToLower(strings.TrimSpace(in.PreferredLanguage))
	in.PreferredTheme = strings.ToLower(strings.TrimSpace(in.PreferredTheme))
	if len(in.DisplayName) > 80 || len(in.PreferredCurrency) != 3 || (in.SecondaryDisplayAsset != "" && in.SecondaryDisplayAsset != "BTC" && in.SecondaryDisplayAsset != "ETH" && in.SecondaryDisplayAsset != "USDT") || len(in.PreferredLanguage) < 2 || len(in.PreferredLanguage) > 12 || (in.PreferredTheme != "system" && in.PreferredTheme != "light" && in.PreferredTheme != "dark") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_preferences"})
		return
	}
	if in.DefaultSlippage != "0.1" && in.DefaultSlippage != "0.5" && in.DefaultSlippage != "1.0" && in.DefaultSlippage != "2.0" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_preferences"})
		return
	}
	profile, err := h.data.UpdateUserPreferences(r.Context(), p.UserID, in.DisplayName, in.PreferredCurrency, in.SecondaryDisplayAsset, in.PreferredLanguage, in.PreferredTheme)
	if err != nil {
		log.Printf("user preferences update failed: user_id=%s error=%v", p.UserID, err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "preferences_unavailable"})
		return
	}
	var settings map[string]any
	if err := json.Unmarshal(body, &settings); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	for key := range settings {
		if key != "email_login" && key != "email_trade" && key != "email_promo" && key != "push_price" && key != "push_withdraw" && key != "confirm_order" && key != "close_position" && key != "default_slippage" && key != "hide_balance" && key != "show_online" {
			delete(settings, key)
		}
	}
	if len(settings) > 0 {
		if err := h.data.UpdateUserPreferencesSettings(r.Context(), p.UserID, settings); err != nil {
			log.Printf("user settings update failed: user_id=%s error=%v", p.UserID, err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "preferences_unavailable"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": profile})
}

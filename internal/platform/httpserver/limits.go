package httpserver

import (
	"net/http"

	"github.com/limiance/backend/internal/datamanager"
)

type CapabilitySet struct {
	CryptoDeposits    bool `json:"crypto_deposits"`
	CryptoTrading     bool `json:"crypto_trading"`
	CryptoWithdrawals bool `json:"crypto_withdrawals"`
	FiatTransfers     bool `json:"fiat_transfers"`
}

type LimitsResponse struct {
	KYCTier      int16         `json:"kyc_tier"`
	Capabilities CapabilitySet `json:"capabilities"`
	DailyLimit   string        `json:"daily_withdrawal_limit,omitempty"`
	MonthlyLimit string        `json:"monthly_trade_limit,omitempty"`
}

func capabilitiesForTier(tier int16) CapabilitySet {
	switch tier {
	case 2:
		return CapabilitySet{
			CryptoDeposits:    true,
			CryptoTrading:     true,
			CryptoWithdrawals: true,
			FiatTransfers:     true,
		}
	case 1:
		return CapabilitySet{
			CryptoDeposits:    true,
			CryptoTrading:     true,
			CryptoWithdrawals: true,
			FiatTransfers:     false,
		}
	default:
		return CapabilitySet{
			CryptoDeposits:    true,
			CryptoTrading:     false,
			CryptoWithdrawals: false,
			FiatTransfers:     false,
		}
	}
}

func limitsResponseForTier(tier int16) LimitsResponse {
	response := LimitsResponse{KYCTier: tier, Capabilities: capabilitiesForTier(tier)}
	switch tier {
	case 2:
		response.DailyLimit = "100000.00"
		response.MonthlyLimit = "1000000.00"
	case 1:
		response.DailyLimit = "5000.00"
		response.MonthlyLimit = "200000.00"
	default:
		response.DailyLimit = "0.00"
		response.MonthlyLimit = "0.00"
	}
	return response
}

type LimitsHandler struct {
	data *datamanager.Manager
}

func NewLimitsHandler(data *datamanager.Manager) *LimitsHandler { return &LimitsHandler{data: data} }

func (h *LimitsHandler) Get(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	response := limitsResponseForTier(0)
	if h.data != nil {
		profile, err := h.data.UserProfile(r.Context(), principal.UserID)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "limits_unavailable"})
			return
		}
		response = limitsResponseForTier(profile.KYCTier)
	}
	writeJSON(w, http.StatusOK, response)
}

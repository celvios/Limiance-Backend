package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/limiance/backend/internal/pnl"
)

type PnLHandler struct {
	service *pnl.Service
	logger  *slog.Logger
}

func NewPnLHandler(service *pnl.Service, logger *slog.Logger) *PnLHandler {
	return &PnLHandler{service: service, logger: logger}
}

func (h *PnLHandler) Get(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	window := 30
	if raw := r.URL.Query().Get("days"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_days"})
			return
		}
		window = parsed
	}
	items, err := h.service.DailyPnL(r.Context(), principal.UserID, window)
	if err != nil {
		h.logger.Error("daily pnl unavailable", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "daily_pnl_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": window, "entries": items})
}

var ErrDailyPnLUnavailable = errors.New("daily pnl unavailable")

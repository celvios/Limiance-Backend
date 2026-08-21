package httpserver

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/limiance/backend/internal/deposits"
)

type DepositHistoryHandler struct {
	service *deposits.HistoryService
	logger  *slog.Logger
}

func NewDepositHistoryHandler(service *deposits.HistoryService, logger *slog.Logger) *DepositHistoryHandler {
	return &DepositHistoryHandler{service: service, logger: logger}
}
func (h *DepositHistoryHandler) List(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	items, err := h.service.List(r.Context(), p.UserID, historyLimit(r))
	if err != nil {
		h.logger.Error("deposit history read failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "deposit_history_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deposits": items})
}
func historyLimit(r *http.Request) int {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit < 1 {
		return 25
	}
	if limit > 100 {
		return 100
	}
	return limit
}

package httpserver

import (
	"net/http"

	"github.com/limiance/backend/internal/fees"
)

type FeesHandler struct {
	service *fees.Service
}

func NewFeesHandler(service *fees.Service) *FeesHandler {
	return &FeesHandler{service: service}
}

func (h *FeesHandler) Get(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	result, err := h.service.GetUserFees(r.Context(), principal.UserID, r.URL.Query().Get("pair"))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "fees_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

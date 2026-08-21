package httpserver

import (
	"log/slog"
	"net/http"

	"github.com/limiance/backend/internal/assets"
)

type AssetHandler struct {
	service *assets.Service
	logger  *slog.Logger
}

func NewAssetHandler(service *assets.Service, logger *slog.Logger) *AssetHandler {
	return &AssetHandler{service: service, logger: logger}
}

func (h *AssetHandler) Catalog(w http.ResponseWriter, r *http.Request) {
	catalog, err := h.service.Catalog(r.Context())
	if err != nil {
		h.logger.Error("asset catalog read failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "catalog_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, catalog)
}

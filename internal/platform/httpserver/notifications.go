package httpserver

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/limiance/backend/internal/notifications"
)

type NotificationHandler struct {
	service *notifications.Service
	logger  *slog.Logger
}

func NewNotificationHandler(service *notifications.Service, logger *slog.Logger) *NotificationHandler {
	return &NotificationHandler{service: service, logger: logger}
}

func (h *NotificationHandler) List(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	items, err := h.service.List(r.Context(), p.UserID, historyLimit(r))
	if err != nil {
		h.logger.Error("notification list failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "notifications_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": items})
}

func (h *NotificationHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	changed, err := h.service.MarkRead(r.Context(), p.UserID, r.PathValue("notification_id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_notification"})
		return
	}
	if !changed {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "notification_not_found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "read"})
}

func (h *NotificationHandler) MarkUnread(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	changed, err := h.service.MarkUnread(r.Context(), p.UserID, r.PathValue("notification_id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_notification"})
		return
	}
	if !changed {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "notification_not_found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "unread"})
}

func (h *NotificationHandler) RegisterDevice(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var input notifications.DeviceInput
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	device, err := h.service.RegisterDevice(r.Context(), p.UserID, input)
	if err != nil {
		if errors.Is(err, notifications.ErrInvalidDevice) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_notification_device"})
			return
		}
		h.logger.Error("notification device registration failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "notification_device_unavailable"})
		return
	}
	writeJSON(w, http.StatusCreated, device)
}

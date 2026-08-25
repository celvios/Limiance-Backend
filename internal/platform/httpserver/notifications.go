package httpserver

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/limiance/backend/internal/notifications"
)

type NotificationHandler struct {
	service *notifications.Service
	logger  *slog.Logger
}

func NewNotificationHandler(service *notifications.Service, logger *slog.Logger) *NotificationHandler {
	return &NotificationHandler{service: service, logger: logger}
}

func (h *NotificationHandler) Create(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	defer r.Body.Close()
	var input notifications.CreateInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	item, created, err := h.service.Create(r.Context(), p.UserID, input)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_notification"})
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"notification": item, "created": created})
}

func (h *NotificationHandler) List(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	limit := historyLimit(r)
	var createdBefore *time.Time
	if raw := r.URL.Query().Get("created_before"); raw != "" {
		parsed, parseErr := time.Parse(time.RFC3339Nano, raw)
		if parseErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_created_before"})
			return
		}
		createdBefore = &parsed
	}
	items, err := h.service.List(r.Context(), p.UserID, limit+1, createdBefore)
	if err != nil {
		h.logger.Error("notification list failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "notifications_unavailable"})
		return
	}
	unread, err := h.service.UnreadCount(r.Context(), p.UserID)
	if err != nil {
		h.logger.Error("notification unread count failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "notifications_unavailable"})
		return
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": items, "unread_count": unread, "limit": limit, "has_more": hasMore})
}

func (h *NotificationHandler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	count, err := h.service.MarkAllRead(r.Context(), p.UserID)
	if err != nil {
		h.logger.Error("mark all notifications read failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "notifications_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"marked_read": count})
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

package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"strings"

	"github.com/limiance/backend/internal/datamanager"
)

var ErrInvalidDevice = errors.New("invalid notification device")
var ErrInvalidNotification = errors.New("invalid notification")

type Service struct{ data *datamanager.Manager }

func NewService(data *datamanager.Manager) *Service { return &Service{data: data} }

func (s *Service) List(ctx context.Context, userID string, limit int, createdBefore *time.Time) ([]datamanager.Notification, error) {
	return s.data.Notifications(ctx, userID, limit, createdBefore)
}

type CreateInput struct {
	EventType   string          `json:"event_type"`
	ReferenceID string          `json:"reference_id"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	Priority    string          `json:"priority"`
	Metadata    json.RawMessage `json:"metadata"`
}

func (s *Service) Create(ctx context.Context, userID string, input CreateInput) (datamanager.Notification, bool, error) {
	input.EventType = strings.TrimSpace(input.EventType)
	input.ReferenceID = strings.TrimSpace(input.ReferenceID)
	input.Title = strings.TrimSpace(input.Title)
	input.Body = strings.TrimSpace(input.Body)
	if userID == "" || input.ReferenceID == "" || input.Title == "" || input.Body == "" || !IsRoutedEvent(input.EventType) || strings.HasPrefix(input.EventType, "email.") {
		return datamanager.Notification{}, false, ErrInvalidNotification
	}
	priority, _ := notificationPriority(input.EventType)
	if input.Priority != "" && input.Priority != priority {
		return datamanager.Notification{}, false, ErrInvalidNotification
	}
	if input.Metadata == nil {
		input.Metadata = json.RawMessage(`{}`)
	}
	return s.data.CreateNotification(ctx, datamanager.NotificationInput{UserID: userID, EventType: input.EventType, ReferenceID: input.ReferenceID, Title: input.Title, Body: input.Body, Priority: priority, Metadata: input.Metadata})
}

func (s *Service) UnreadCount(ctx context.Context, userID string) (int64, error) {
	return s.data.UnreadNotificationCount(ctx, userID)
}

func (s *Service) MarkRead(ctx context.Context, userID, notificationID string) (bool, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(notificationID) == "" {
		return false, ErrInvalidDevice
	}
	return s.data.MarkNotificationRead(ctx, userID, notificationID)
}

func (s *Service) MarkUnread(ctx context.Context, userID, notificationID string) (bool, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(notificationID) == "" {
		return false, ErrInvalidDevice
	}
	return s.data.MarkNotificationUnread(ctx, userID, notificationID)
}

func (s *Service) MarkAllRead(ctx context.Context, userID string) (int64, error) {
	if strings.TrimSpace(userID) == "" {
		return 0, ErrInvalidDevice
	}
	return s.data.MarkAllNotificationsRead(ctx, userID)
}

type DeviceInput struct {
	Platform string `json:"platform"`
	Token    string `json:"token"`
}

func (s *Service) RegisterDevice(ctx context.Context, userID string, input DeviceInput) (datamanager.NotificationDevice, error) {
	input.Platform = strings.ToLower(strings.TrimSpace(input.Platform))
	input.Token = strings.TrimSpace(input.Token)
	if userID == "" || (input.Platform != "fcm" && input.Platform != "apns") || len(input.Token) < 20 || len(input.Token) > 4096 {
		return datamanager.NotificationDevice{}, ErrInvalidDevice
	}
	return s.data.UpsertNotificationDevice(ctx, userID, input.Platform, input.Token)
}

package notifications

import (
	"context"
	"errors"
	"strings"

	"github.com/limiance/backend/internal/datamanager"
)

var ErrInvalidDevice = errors.New("invalid notification device")

type Service struct{ data *datamanager.Manager }

func NewService(data *datamanager.Manager) *Service { return &Service{data: data} }

func (s *Service) List(ctx context.Context, userID string, limit int) ([]datamanager.Notification, error) {
	return s.data.Notifications(ctx, userID, limit)
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

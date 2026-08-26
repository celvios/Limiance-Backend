package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/platform/queue"
	"github.com/limiance/backend/internal/security/envelope"
)

var ErrNotificationStoreUnavailable = errors.New("notification store is unavailable")

// NotificationStore is deliberately small so the queue worker can be tested
// without a database and cannot reach unrelated ledger tables.
type NotificationStore interface {
	CreateNotification(context.Context, datamanager.NotificationInput) (datamanager.Notification, bool, error)
	NotificationRecipients(context.Context, queue.Event, map[string]any) ([]datamanager.NotificationRecipient, error)
}

type notificationDeviceStore interface {
	NotificationDeviceRecipients(context.Context, string) ([]datamanager.NotificationDeviceRecipient, error)
}

type notificationDeliveryStore interface {
	NotificationDeliveryComplete(context.Context, string, string, string) (bool, error)
	MarkNotificationDeliveryComplete(context.Context, string, string, string) error
	RecordNotificationDeliveryAttempt(context.Context, string, string, string) error
}

func notificationPriority(eventType string) (string, bool) {
	switch {
	case strings.HasPrefix(eventType, "security."):
		return "urgent", true
	case strings.HasPrefix(eventType, "deposit."), strings.HasPrefix(eventType, "withdrawal."):
		return "high", true
	case eventType == "transfer.completed":
		return "normal", false
	default:
		return "", false
	}
}

func (c *Consumer) processNotification(ctx context.Context, event queue.Event) error {
	store, ok := c.store.(NotificationStore)
	if !ok || store == nil {
		return ErrNotificationStoreUnavailable
	}
	priority, emailRequired := notificationPriority(event.Type)
	copy, ok := TemplateForEvent(event.Type)
	if !ok {
		return ErrUnexpectedEvent
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	if event.Type == "security.anti_phishing_code_enabled" {
		ciphertext, ok := payload["code_ciphertext"].(string)
		if !ok || ciphertext == "" {
			return ErrNotificationStoreUnavailable
		}
		code, err := envelope.Open(c.encryptionKey, ciphertext)
		if err != nil {
			return err
		}
		copy.Body += "\n\nYour anti-phishing code: " + code + "\nNever share this code with anyone."
	}
	recipients, err := store.NotificationRecipients(ctx, event, payload)
	if err != nil {
		return err
	}
	if len(recipients) == 0 {
		return ErrNotificationStoreUnavailable
	}
	for _, recipient := range recipients {
		item, _, err := store.CreateNotification(ctx, datamanager.NotificationInput{
			UserID: recipient.UserID, EventType: event.Type, ReferenceID: event.AggregateID,
			Title: copy.Title, Body: copy.Body, Priority: priority, Metadata: event.Payload,
		})
		if err != nil {
			return err
		}
		deliveryStore, ok := store.(notificationDeliveryStore)
		if !ok {
			return ErrNotificationStoreUnavailable
		}
		if emailRequired {
			sender, ok := c.email.(TransactionalEmailSender)
			if !ok {
				return ErrSendGridNotConfigured
			}
			delivered, err := deliveryStore.NotificationDeliveryComplete(ctx, item.ID, "email", recipient.Email)
			if err != nil {
				return err
			}
			if !delivered {
				if err := deliveryStore.RecordNotificationDeliveryAttempt(ctx, item.ID, "email", recipient.Email); err != nil {
					return err
				}
				emailCopy := copy
				if recipient.AntiPhishingCodeCiphertext != "" {
					code, err := envelope.Open(c.encryptionKey, recipient.AntiPhishingCodeCiphertext)
					if err != nil {
						return err
					}
					emailCopy.Body += "\n\nYour anti-phishing code: " + code + "\nLimiance support will never ask you to share this code."
				}
				if err := sender.SendTransactionalEmail(ctx, recipient.Email, emailCopy); err != nil {
					return err
				}
				if err := deliveryStore.MarkNotificationDeliveryComplete(ctx, item.ID, "email", recipient.Email); err != nil {
					return err
				}
			}
		}
		if priority == "urgent" {
			if c.push == nil {
				return ErrNotificationStoreUnavailable
			}
			deviceStore, ok := store.(notificationDeviceStore)
			if !ok {
				return ErrNotificationStoreUnavailable
			}
			devices, err := deviceStore.NotificationDeviceRecipients(ctx, recipient.UserID)
			if err != nil {
				return err
			}
			for _, device := range devices {
				delivered, err := deliveryStore.NotificationDeliveryComplete(ctx, item.ID, "push", device.Platform+":"+device.Token)
				if err != nil {
					return err
				}
				if delivered {
					continue
				}
				if err := deliveryStore.RecordNotificationDeliveryAttempt(ctx, item.ID, "push", device.Platform+":"+device.Token); err != nil {
					return err
				}
				if err := c.push.SendPush(ctx, device.Platform, device.Token, event.Type, copy); err != nil {
					return err
				}
				if err := deliveryStore.MarkNotificationDeliveryComplete(ctx, item.ID, "push", device.Platform+":"+device.Token); err != nil {
					return err
				}
			}
		}
		_ = item
	}
	return nil
}

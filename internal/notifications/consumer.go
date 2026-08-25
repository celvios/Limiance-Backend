package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/limiance/backend/internal/platform/queue"
	"github.com/limiance/backend/internal/security/envelope"
)

type SQSReceiver interface {
	Receive(context.Context, int32) ([]queue.ReceivedEvent, error)
	Delete(context.Context, string) error
}

type EmailVerifier interface {
	SendEmailVerification(context.Context, string, string, time.Time) error
	SendPasswordReset(context.Context, string, string, time.Time) error
}

type TransactionalEmailSender interface {
	SendTransactionalEmail(context.Context, string, TransactionalEmail) error
}

type PushSender interface {
	SendPush(context.Context, string, string, string, TransactionalEmail) error
}

type stubPushSender struct{}

func (stubPushSender) SendPush(context.Context, string, string, string, TransactionalEmail) error {
	// TODO: deliver urgent notifications through APNs/FCM.
	return nil
}

type Consumer struct {
	queue         SQSReceiver
	email         EmailVerifier
	push          PushSender
	encryptionKey string
	store         any
}

func (c *Consumer) WithStore(store NotificationStore) *Consumer {
	c.store = store
	return c
}

var ErrUnexpectedEvent = errors.New("unexpected event on notifications queue")

func NewConsumer(receiver SQSReceiver, email EmailVerifier, encryptionKey string) *Consumer {
	return &Consumer{queue: receiver, email: email, push: stubPushSender{}, encryptionKey: encryptionKey}
}

func (c *Consumer) RunOnce(ctx context.Context) (int, error) {
	messages, err := c.queue.Receive(ctx, 10)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, message := range messages {
		if message.Event.Type != "email.verification_requested" && message.Event.Type != "email.password_reset_requested" && !IsRoutedEvent(message.Event.Type) {
			return processed, ErrUnexpectedEvent
		}
		if message.Event.Type != "email.verification_requested" && message.Event.Type != "email.password_reset_requested" {
			if err := c.processNotification(ctx, message.Event); err != nil {
				return processed, err
			}
			if err := c.queue.Delete(ctx, message.ReceiptHandle); err != nil {
				return processed, err
			}
			processed++
			continue
		}
		var payload struct {
			Email          string `json:"email"`
			CodeCiphertext string `json:"code_ciphertext"`
			ExpiresAt      string `json:"expires_at"`
		}
		if err := json.Unmarshal(message.Event.Payload, &payload); err != nil {
			return processed, err
		}
		code, err := envelope.Open(c.encryptionKey, payload.CodeCiphertext)
		if err != nil {
			return processed, err
		}
		expiresAt, err := time.Parse(time.RFC3339, payload.ExpiresAt)
		if err != nil || expiresAt.Before(time.Now()) {
			// An expired challenge is terminal: it must never be delivered, and
			// leaving it on an at-least-once queue would block newer verification
			// emails behind the same poison message forever.
			if err := c.queue.Delete(ctx, message.ReceiptHandle); err != nil {
				return processed, err
			}
			continue
		}
		var sendErr error
		if message.Event.Type == "email.password_reset_requested" {
			sendErr = c.email.SendPasswordReset(ctx, payload.Email, code, expiresAt)
		} else {
			sendErr = c.email.SendEmailVerification(ctx, payload.Email, code, expiresAt)
		}
		if sendErr != nil {
			return processed, sendErr
		}
		if err := c.queue.Delete(ctx, message.ReceiptHandle); err != nil {
			return processed, err
		}
		processed++
	}
	return processed, nil
}

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
}

type Consumer struct {
	queue         SQSReceiver
	email         EmailVerifier
	encryptionKey string
}

var ErrUnexpectedEvent = errors.New("unexpected event on notifications queue")

func NewConsumer(receiver SQSReceiver, email EmailVerifier, encryptionKey string) *Consumer {
	return &Consumer{queue: receiver, email: email, encryptionKey: encryptionKey}
}

func (c *Consumer) RunOnce(ctx context.Context) (int, error) {
	messages, err := c.queue.Receive(ctx, 10)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, message := range messages {
		if message.Event.Type != "email.verification_requested" {
			return processed, ErrUnexpectedEvent
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
		if err := c.email.SendEmailVerification(ctx, payload.Email, code, expiresAt); err != nil {
			return processed, err
		}
		if err := c.queue.Delete(ctx, message.ReceiptHandle); err != nil {
			return processed, err
		}
		processed++
	}
	return processed, nil
}

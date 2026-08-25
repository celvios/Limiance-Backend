package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/platform/queue"
)

type notificationReceiverStub struct {
	messages []queue.ReceivedEvent
	deleted  []string
}

func (s *notificationReceiverStub) Receive(context.Context, int32) ([]queue.ReceivedEvent, error) {
	return s.messages, nil
}
func (s *notificationReceiverStub) Delete(_ context.Context, receipt string) error {
	s.deleted = append(s.deleted, receipt)
	return nil
}

type notificationEmailStub struct{ transactional int }

type retryEmailStub struct {
	transactional int
	failures      int
}

func (s *retryEmailStub) SendEmailVerification(context.Context, string, string, time.Time) error {
	return nil
}
func (s *retryEmailStub) SendPasswordReset(context.Context, string, string, time.Time) error {
	return nil
}
func (s *retryEmailStub) SendTransactionalEmail(context.Context, string, TransactionalEmail) error {
	s.transactional++
	if s.failures > 0 {
		s.failures--
		return errors.New("email provider unavailable")
	}
	return nil
}

type notificationPushStub struct {
	platform string
	token    string
	event    string
}

func (s *notificationPushStub) SendPush(_ context.Context, platform, token, event string, _ TransactionalEmail) error {
	s.platform, s.token, s.event = platform, token, event
	return nil
}

func (s *notificationEmailStub) SendEmailVerification(context.Context, string, string, time.Time) error {
	return nil
}
func (s *notificationEmailStub) SendPasswordReset(context.Context, string, string, time.Time) error {
	return nil
}
func (s *notificationEmailStub) SendTransactionalEmail(context.Context, string, TransactionalEmail) error {
	s.transactional++
	return nil
}

type notificationStoreStub struct {
	created bool
	input   datamanager.NotificationInput
	delivered map[string]bool
}

func (s *notificationStoreStub) CreateNotification(_ context.Context, input datamanager.NotificationInput) (datamanager.Notification, bool, error) {
	s.input = input
	return datamanager.Notification{ID: "notification-1"}, s.created, nil
}
func (s *notificationStoreStub) NotificationRecipients(context.Context, queue.Event, map[string]any) ([]datamanager.NotificationRecipient, error) {
	return []datamanager.NotificationRecipient{{UserID: "user-1", Email: "user@example.test"}}, nil
}
func (s *notificationStoreStub) NotificationDeviceRecipients(context.Context, string) ([]datamanager.NotificationDeviceRecipient, error) {
	return []datamanager.NotificationDeviceRecipient{{Platform: "fcm", Token: "fcm-token-12345678901234567890"}}, nil
}
func (s *notificationStoreStub) NotificationDeliveryComplete(_ context.Context, _, channel, destination string) (bool, error) {
	return s.delivered[channel+":"+destination], nil
}
func (s *notificationStoreStub) MarkNotificationDeliveryComplete(_ context.Context, _, channel, destination string) error {
	if s.delivered == nil {
		s.delivered = map[string]bool{}
	}
	s.delivered[channel+":"+destination] = true
	return nil
}
func (s *notificationStoreStub) RecordNotificationDeliveryAttempt(context.Context, string, string, string) error {
	return nil
}

func TestConsumerPersistsAndDeliversHighPriorityNotificationOnce(t *testing.T) {
	receiver := &notificationReceiverStub{messages: []queue.ReceivedEvent{{
		ReceiptHandle: "receipt-1",
		Event:         queue.Event{ID: "event-1", Type: "deposit.credited", AggregateID: "deposit-1", Payload: json.RawMessage(`{"amount_atomic":"42"}`)},
	}}}
	email := &notificationEmailStub{}
	store := &notificationStoreStub{created: true}
	consumer := NewConsumer(receiver, email, "unused").WithStore(store)
	push := &notificationPushStub{}
	consumer.push = push
	count, err := consumer.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || email.transactional != 1 || len(receiver.deleted) != 1 {
		t.Fatalf("unexpected delivery result: count=%d emails=%d deletes=%d", count, email.transactional, len(receiver.deleted))
	}
	if store.input.EventType != "deposit.credited" || store.input.ReferenceID != "deposit-1" || store.input.Priority != "high" {
		t.Fatalf("unexpected idempotency input: %+v", store.input)
	}

	// A redelivery finds the existing in-app notification and must not emit a
	// second transactional email before it acknowledges the queue message.
	store.created = false
	receiver.deleted = nil
	count, err = consumer.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || email.transactional != 1 || len(receiver.deleted) != 1 {
		t.Fatalf("retry duplicated delivery: count=%d emails=%d deletes=%d", count, email.transactional, len(receiver.deleted))
	}
}

func TestConsumerTargetsRegisteredPushTokenForUrgentNotification(t *testing.T) {
	receiver := &notificationReceiverStub{messages: []queue.ReceivedEvent{{
		ReceiptHandle: "receipt-security",
		Event:         queue.Event{ID: "event-security", Type: "security.password_changed", AggregateID: "user-1", Payload: json.RawMessage(`{"user_id":"user-1"}`)},
	}}}
	store := &notificationStoreStub{created: true}
	consumer := NewConsumer(&notificationReceiverStub{messages: receiver.messages}, &notificationEmailStub{}, "unused").WithStore(store)
	push := &notificationPushStub{}
	consumer.push = push
	if _, err := consumer.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if push.platform != "fcm" || push.token != "fcm-token-12345678901234567890" || push.event != "security.password_changed" {
		t.Fatalf("unexpected push target: %+v", push)
	}
}

func TestConsumerPersistsNormalTransferWithoutEmail(t *testing.T) {
	receiver := &notificationReceiverStub{messages: []queue.ReceivedEvent{{
		ReceiptHandle: "receipt-transfer",
		Event:         queue.Event{ID: "event-transfer", Type: "transfer.completed", AggregateID: "transfer-1", Payload: json.RawMessage(`{}`)},
	}}}
	email := &notificationEmailStub{}
	store := &notificationStoreStub{created: true}
	consumer := NewConsumer(receiver, email, "unused").WithStore(store)
	count, err := consumer.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || email.transactional != 0 || store.input.Priority != "normal" {
		t.Fatalf("unexpected normal transfer delivery: count=%d emails=%d priority=%s", count, email.transactional, store.input.Priority)
	}
}

func TestConsumerRetriesEmailAfterProviderFailure(t *testing.T) {
	receiver := &notificationReceiverStub{messages: []queue.ReceivedEvent{{
		ReceiptHandle: "receipt-retry",
		Event:         queue.Event{ID: "event-retry", Type: "deposit.credited", AggregateID: "deposit-1", Payload: json.RawMessage(`{"amount_atomic":"42"}`)},
	}}}
	email := &retryEmailStub{failures: 1}
	store := &notificationStoreStub{created: true, delivered: map[string]bool{}}
	consumer := NewConsumer(receiver, email, "unused").WithStore(store)
	if _, err := consumer.RunOnce(context.Background()); err == nil {
		t.Fatal("expected first email attempt to fail")
	}
	if len(receiver.deleted) != 0 {
		t.Fatal("failed delivery must remain queued")
	}
	store.created = false
	if _, err := consumer.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if email.transactional != 2 || len(receiver.deleted) != 1 {
		t.Fatalf("email was not retried correctly: attempts=%d deletes=%d", email.transactional, len(receiver.deleted))
	}
}

package eventrouter

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/limiance/backend/internal/platform/queue"
)

type receiverStub struct {
	messages []queue.ReceivedEvent
	deleted  []string
}

func (s *receiverStub) Receive(_ context.Context, _ int32) ([]queue.ReceivedEvent, error) {
	return s.messages, nil
}
func (s *receiverStub) Delete(_ context.Context, receipt string) error {
	s.deleted = append(s.deleted, receipt)
	return nil
}

type publisherStub struct {
	events []queue.Event
	err    error
}

func (s *publisherStub) Publish(_ context.Context, event queue.Event) error {
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, event)
	return nil
}

func TestRunOnceRoutesNotificationsAndQuarantinesOtherEvents(t *testing.T) {
	notifications := &publisherStub{}
	quarantine := &publisherStub{}
	source := &receiverStub{messages: []queue.ReceivedEvent{
		{ReceiptHandle: "notification", Event: queue.Event{ID: "one", Type: "email.verification_requested", Payload: json.RawMessage(`{}`)}},
		{ReceiptHandle: "other", Event: queue.Event{ID: "two", Type: "user.registered", Payload: json.RawMessage(`{}`)}},
	}}
	router, err := New(source, map[string]queue.Publisher{"email.verification_requested": notifications}, quarantine)
	if err != nil {
		t.Fatal(err)
	}
	count, err := router.RunOnce(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || len(notifications.events) != 1 || len(quarantine.events) != 1 || len(source.deleted) != 2 {
		t.Fatalf("unexpected routing result: count=%d notifications=%d quarantine=%d deleted=%d", count, len(notifications.events), len(quarantine.events), len(source.deleted))
	}
}

func TestRunOnceDoesNotAcknowledgeWhenPublishFails(t *testing.T) {
	source := &receiverStub{messages: []queue.ReceivedEvent{{ReceiptHandle: "receipt", Event: queue.Event{Type: "email.verification_requested"}}}}
	router, err := New(source, map[string]queue.Publisher{"email.verification_requested": &publisherStub{err: errors.New("destination down")}}, &publisherStub{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.RunOnce(context.Background(), 1); err == nil {
		t.Fatal("expected publish error")
	}
	if len(source.deleted) != 0 {
		t.Fatal("source event must remain for retry")
	}
}

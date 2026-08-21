// Package eventrouter fans shared outbox events into dedicated worker queues.
// It preserves at-least-once delivery: the source message is acknowledged only
// after its selected destination accepts a copy.
package eventrouter

import (
	"context"
	"errors"

	"github.com/limiance/backend/internal/platform/queue"
)

var ErrNoDestination = errors.New("event router has no destination")

type Receiver interface {
	Receive(context.Context, int32) ([]queue.ReceivedEvent, error)
	Delete(context.Context, string) error
}

type Router struct {
	source   Receiver
	routes   map[string]queue.Publisher
	unrouted queue.Publisher
}

// New builds a router with explicit event-type destinations. unrouted is a
// required quarantine destination for events that have not yet been assigned a
// worker queue, keeping them durable and visible for operations.
func New(source Receiver, routes map[string]queue.Publisher, unrouted queue.Publisher) (*Router, error) {
	if source == nil || unrouted == nil {
		return nil, ErrNoDestination
	}
	copyRoutes := make(map[string]queue.Publisher, len(routes))
	for eventType, destination := range routes {
		if eventType == "" || destination == nil {
			return nil, ErrNoDestination
		}
		copyRoutes[eventType] = destination
	}
	return &Router{source: source, routes: copyRoutes, unrouted: unrouted}, nil
}

func (r *Router) RunOnce(ctx context.Context, batchSize int32) (int, error) {
	if batchSize < 1 || batchSize > 10 {
		return 0, errors.New("router batch size must be between 1 and 10")
	}
	messages, err := r.source.Receive(ctx, batchSize)
	if err != nil {
		return 0, err
	}
	routed := 0
	for _, message := range messages {
		destination := r.routes[message.Event.Type]
		if destination == nil {
			destination = r.unrouted
		}
		if err := destination.Publish(ctx, message.Event); err != nil {
			return routed, err
		}
		if err := r.source.Delete(ctx, message.ReceiptHandle); err != nil {
			return routed, err
		}
		routed++
	}
	return routed, nil
}

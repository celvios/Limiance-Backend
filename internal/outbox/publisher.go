// Package outbox publishes committed domain events to durable transport.
package outbox

import (
	"context"

	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/platform/queue"
)

type Publisher struct {
	data  *datamanager.Manager
	queue queue.Publisher
}

func NewPublisher(data *datamanager.Manager, transport queue.Publisher) *Publisher {
	return &Publisher{data: data, queue: transport}
}

func (p *Publisher) RunOnce(ctx context.Context, batchSize int) (int, error) {
	events, err := p.data.LeaseOutbox(ctx, batchSize)
	if err != nil {
		return 0, err
	}
	published := 0
	for _, event := range events {
		if err := p.queue.Publish(ctx, queue.Event{ID: event.ID, Type: event.EventType, AggregateType: event.AggregateType, AggregateID: event.AggregateID, Payload: event.Payload}); err != nil {
			return published, err
		}
		marked, err := p.data.MarkOutboxPublished(ctx, event.ID, event.LeaseToken)
		if err != nil {
			return published, err
		}
		if marked {
			published++
		}
	}
	return published, nil
}

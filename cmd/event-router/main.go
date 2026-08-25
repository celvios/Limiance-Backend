package main

import (
	"context"
	"log"
	"time"

	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/eventrouter"
	"github.com/limiance/backend/internal/notifications"
	"github.com/limiance/backend/internal/platform/queue"
)

func main() {
	cfg := config.Load()
	ctx := context.Background()
	source, err := queue.NewSQS(ctx, queue.SQSConfig{Region: cfg.AWSRegion, QueueURL: cfg.SQSOutboxQueueURL, Endpoint: cfg.SQSEndpoint})
	if err != nil {
		log.Fatal(err)
	}
	notificationQueue, err := queue.NewSQS(ctx, queue.SQSConfig{Region: cfg.AWSRegion, QueueURL: cfg.SQSNotificationsQueueURL, Endpoint: cfg.SQSEndpoint})
	if err != nil {
		log.Fatal(err)
	}
	deposits, err := queue.NewSQS(ctx, queue.SQSConfig{Region: cfg.AWSRegion, QueueURL: cfg.SQSDepositsQueueURL, Endpoint: cfg.SQSEndpoint})
	if err != nil {
		log.Fatal(err)
	}
	unrouted, err := queue.NewSQS(ctx, queue.SQSConfig{Region: cfg.AWSRegion, QueueURL: cfg.SQSUnroutedQueueURL, Endpoint: cfg.SQSEndpoint})
	if err != nil {
		log.Fatal(err)
	}
	routes := map[string]queue.Publisher{
		"custody.webhook_received": deposits,
	}
	for _, eventType := range notifications.RoutedEventTypes {
		routes[eventType] = notificationQueue
	}
	router, err := eventrouter.New(source, routes, unrouted)
	if err != nil {
		log.Fatal(err)
	}
	for {
		if _, err := router.RunOnce(ctx, 10); err != nil {
			log.Printf("event routing failed: %v", err)
			time.Sleep(5 * time.Second)
		}
	}
}

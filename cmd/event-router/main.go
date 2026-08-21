package main

import (
	"context"
	"log"
	"time"

	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/eventrouter"
	"github.com/limiance/backend/internal/platform/queue"
)

func main() {
	cfg := config.Load()
	ctx := context.Background()
	source, err := queue.NewSQS(ctx, queue.SQSConfig{Region: cfg.AWSRegion, QueueURL: cfg.SQSOutboxQueueURL, Endpoint: cfg.SQSEndpoint})
	if err != nil {
		log.Fatal(err)
	}
	notifications, err := queue.NewSQS(ctx, queue.SQSConfig{Region: cfg.AWSRegion, QueueURL: cfg.SQSNotificationsQueueURL, Endpoint: cfg.SQSEndpoint})
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
	router, err := eventrouter.New(source, map[string]queue.Publisher{"email.verification_requested": notifications, "custody.webhook_received": deposits}, unrouted)
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

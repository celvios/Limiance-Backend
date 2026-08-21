package main

import (
	"context"
	"log"
	"time"

	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/outbox"
	"github.com/limiance/backend/internal/platform/database"
	"github.com/limiance/backend/internal/platform/queue"
)

func main() {
	cfg := config.Load()
	ctx := context.Background()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	transport, err := queue.NewSQS(ctx, queue.SQSConfig{Region: cfg.AWSRegion, QueueURL: cfg.SQSOutboxQueueURL, Endpoint: cfg.SQSEndpoint})
	if err != nil {
		log.Fatal(err)
	}
	publisher := outbox.NewPublisher(datamanager.New(pool), transport)
	for {
		published, err := publisher.RunOnce(ctx, 25)
		if err != nil {
			log.Printf("outbox publish failed: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		if published == 0 {
			time.Sleep(time.Second)
		}
	}
}

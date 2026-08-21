package main

import (
	"context"
	"log"
	"time"

	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/deposits"
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
	receiver, err := queue.NewSQS(ctx, queue.SQSConfig{Region: cfg.AWSRegion, QueueURL: cfg.SQSDepositsQueueURL, Endpoint: cfg.SQSEndpoint})
	if err != nil {
		log.Fatal(err)
	}
	worker := deposits.NewWorker(receiver, datamanager.New(pool))
	for {
		if _, err := worker.RunOnce(ctx); err != nil {
			log.Printf("deposit worker failed: %v", err)
			time.Sleep(5 * time.Second)
		}
	}
}

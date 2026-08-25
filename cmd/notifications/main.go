package main

import (
	"context"
	"log"
	"time"

	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/notifications"
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
	transport, err := queue.NewSQS(ctx, queue.SQSConfig{Region: cfg.AWSRegion, QueueURL: cfg.SQSNotificationsQueueURL, Endpoint: cfg.SQSEndpoint})
	if err != nil {
		log.Fatal(err)
	}
	email, err := notifications.NewSendGrid(notifications.SendGridConfig{APIKey: cfg.SendGridAPIKey, FromEmail: cfg.SendGridFromEmail, TemplateID: cfg.SendGridTemplate})
	if err != nil {
		log.Fatal(err)
	}
	consumer := notifications.NewConsumer(transport, email, cfg.VerificationEncryptionKey).WithStore(datamanager.New(pool))
	for {
		if _, err := consumer.RunOnce(ctx); err != nil {
			log.Printf("notification processing failed: %v", err)
			time.Sleep(5 * time.Second)
		}
	}
}

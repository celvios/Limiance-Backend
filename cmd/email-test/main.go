package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/notifications"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: go run ./cmd/email-test recipient@example.com")
	}
	cfg := config.Load()
	provider, err := notifications.NewSendGrid(notifications.SendGridConfig{APIKey: cfg.SendGridAPIKey, FromEmail: cfg.SendGridFromEmail})
	if err != nil {
		log.Fatal(err)
	}
	if err := provider.SendTestEmail(context.Background(), os.Args[1]); err != nil {
		log.Fatal(err)
	}
	fmt.Println("SendGrid accepted the Limiance email delivery test")
}

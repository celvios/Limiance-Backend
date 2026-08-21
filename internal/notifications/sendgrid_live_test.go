package notifications_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/notifications"
)

// TestSendGridLive sends one real email only when explicitly enabled. It is
// skipped by normal test runs and is intended for a verified sandbox sender.
func TestSendGridLive(t *testing.T) {
	if os.Getenv("SENDGRID_LIVE_TEST") != "1" {
		t.Skip("set SENDGRID_LIVE_TEST=1 to send a real verification email")
	}
	cfg := config.Load()
	provider, err := notifications.NewSendGrid(notifications.SendGridConfig{
		APIKey:     cfg.SendGridAPIKey,
		FromEmail:  cfg.SendGridFromEmail,
		TemplateID: cfg.SendGridTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	recipient := os.Getenv("SENDGRID_TEST_RECIPIENT")
	if recipient == "" {
		recipient = cfg.SendGridFromEmail
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := provider.SendEmailVerification(ctx, recipient, "123456", time.Now().Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
}

package notifications_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/notifications"
)

// TestTwilioVerifyLive starts one real SMS verification only when explicitly
// enabled. Twilio trial accounts require the recipient to be verified first.
func TestTwilioVerifyLive(t *testing.T) {
	if os.Getenv("TWILIO_LIVE_TEST") != "1" {
		t.Skip("set TWILIO_LIVE_TEST=1 to send a real Twilio Verify SMS")
	}
	cfg := config.Load()
	provider, err := notifications.NewTwilioVerify(notifications.TwilioVerifyConfig{
		APIKey:     cfg.TwilioAPIKey,
		APISecret:  cfg.TwilioAPISecret,
		ServiceSID: cfg.TwilioVerifySID,
	})
	if err != nil {
		t.Fatal(err)
	}
	phone := os.Getenv("TWILIO_TEST_RECIPIENT")
	if phone == "" {
		t.Fatal("set TWILIO_TEST_RECIPIENT to a Twilio trial-approved E.164 number")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	verification, err := provider.StartSMS(ctx, phone)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Twilio Verify accepted the SMS request (%s, status=%s)", verification.SID, verification.Status)
}

// TestTwilioVerifyLiveCheck confirms the code from a preceding live test. It
// is separately gated so normal runs never consume a verification attempt.
func TestTwilioVerifyLiveCheck(t *testing.T) {
	if os.Getenv("TWILIO_LIVE_CHECK") != "1" {
		t.Skip("set TWILIO_LIVE_CHECK=1 to check a received Twilio Verify code")
	}
	cfg := config.Load()
	provider, err := notifications.NewTwilioVerify(notifications.TwilioVerifyConfig{
		APIKey:     cfg.TwilioAPIKey,
		APISecret:  cfg.TwilioAPISecret,
		ServiceSID: cfg.TwilioVerifySID,
	})
	if err != nil {
		t.Fatal(err)
	}
	phone, code := os.Getenv("TWILIO_TEST_RECIPIENT"), os.Getenv("TWILIO_TEST_CODE")
	if phone == "" || code == "" {
		t.Fatal("set TWILIO_TEST_RECIPIENT and TWILIO_TEST_CODE in local .env")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	verification, err := provider.CheckSMS(ctx, phone, code)
	if err != nil {
		t.Fatal(err)
	}
	if verification.Status != "approved" {
		t.Fatalf("expected approved verification, got %q", verification.Status)
	}
}

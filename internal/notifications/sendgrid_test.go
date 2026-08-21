package notifications

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSendGridVerificationRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v3/mail/send" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("unexpected authorization %q", got)
		}
		var payload struct {
			Subject string `json:"subject"`
			Content []struct {
				Type  string `json:"type"`
				Value string `json:"value"`
			} `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Subject != "Your Limiance verification code" || len(payload.Content) != 2 || payload.Content[0].Value == "" || payload.Content[1].Value == "" {
			t.Fatal("verification email content was not sent")
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	provider, err := NewSendGrid(SendGridConfig{APIKey: "test-key", FromEmail: "security@limiance.test", Endpoint: server.URL + "/v3/mail/send"})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.SendEmailVerification(context.Background(), "user@limiance.test", "123456", time.Now().Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func TestSendGridRequiresProductionConfiguration(t *testing.T) {
	if _, err := NewSendGrid(SendGridConfig{}); err != ErrSendGridNotConfigured {
		t.Fatalf("expected missing configuration error, got %v", err)
	}
}

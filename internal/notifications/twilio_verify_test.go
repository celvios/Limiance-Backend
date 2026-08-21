package notifications

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestTwilioVerifyStartAndCheck(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		user, secret, ok := r.BasicAuth()
		if !ok || user != "SKtest" || secret != "test-secret" {
			t.Fatal("expected API key basic authentication")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		switch r.URL.Path {
		case "/Verifications":
			if form.Get("To") != "+2348012345678" || form.Get("Channel") != "sms" {
				t.Fatal("unexpected start request")
			}
			_, _ = w.Write([]byte(`{"sid":"VEtest","status":"pending"}`))
		case "/VerificationCheck":
			if form.Get("To") != "+2348012345678" || form.Get("Code") != "123456" {
				t.Fatal("unexpected check request")
			}
			_, _ = w.Write([]byte(`{"sid":"VEtest","status":"approved"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	provider, err := NewTwilioVerify(TwilioVerifyConfig{APIKey: "SKtest", APISecret: "test-secret", ServiceSID: "VAservice", Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	started, err := provider.StartSMS(context.Background(), "+2348012345678")
	if err != nil || started.Status != "pending" {
		t.Fatalf("start failed: %#v, %v", started, err)
	}
	checked, err := provider.CheckSMS(context.Background(), "+2348012345678", "123456")
	if err != nil || checked.Status != "approved" || requests != 2 {
		t.Fatalf("check failed: %#v, %v", checked, err)
	}
}

func TestTwilioVerifyRejectsInvalidPhone(t *testing.T) {
	provider, err := NewTwilioVerify(TwilioVerifyConfig{APIKey: "key", APISecret: "secret", ServiceSID: "service", Endpoint: "http://127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.StartSMS(context.Background(), "08012345678"); err != ErrInvalidPhone {
		t.Fatalf("expected E.164 validation error, got %v", err)
	}
}

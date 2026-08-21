package kyc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSumsubClientSignsApplicantAndSDKRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		ts := r.Header.Get("X-App-Access-Ts")
		mac := hmac.New(sha256.New, []byte("secret"))
		_, _ = mac.Write(append([]byte(ts+r.Method+r.URL.RequestURI()), body...))
		if got := r.Header.Get("X-App-Access-Sig"); got != hex.EncodeToString(mac.Sum(nil)) {
			t.Fatal("invalid Sumsub HMAC signature")
		}
		if r.Header.Get("X-App-Token") != "app-token" {
			t.Fatal("missing app token")
		}
		switch r.URL.Path {
		case "/resources/applicants":
			_, _ = w.Write([]byte(`{"id":"applicant-1"}`))
		case "/resources/accessTokens/sdk":
			_, _ = w.Write([]byte(`{"token":"sdk-token"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{AppToken: "app-token", SecretKey: "secret", LevelName: "basic-kyc", Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	applicant, err := client.CreateApplicant(context.Background(), "limiance-user-1", "user@limiance.test")
	if err != nil || applicant.ID != "applicant-1" {
		t.Fatalf("applicant=%#v err=%v", applicant, err)
	}
	token, err := client.CreateAccessToken(context.Background(), "limiance-user-1", "user@limiance.test")
	if err != nil || token != "sdk-token" {
		t.Fatalf("token=%q err=%v", token, err)
	}
}

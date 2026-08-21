package kyc

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestVerifyWebhook(t *testing.T) {
	body := []byte(`{"type":"applicantReviewed"}`)
	mac := hmac.New(sha256.New, []byte("webhook-secret"))
	_, _ = mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))
	if err := VerifyWebhook("webhook-secret", "HMAC_SHA256_HEX", signature, body); err != nil {
		t.Fatal(err)
	}
	if err := VerifyWebhook("webhook-secret", "HMAC_SHA256_HEX", signature, []byte("tampered")); err == nil {
		t.Fatal("expected invalid signature")
	}
}

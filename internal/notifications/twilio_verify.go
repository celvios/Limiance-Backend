package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	ErrTwilioNotConfigured = errors.New("Twilio Verify is not configured")
	ErrInvalidPhone        = errors.New("phone number must be E.164")
)

const twilioVerifyBaseURL = "https://verify.twilio.com/v2/Services/"

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

type TwilioVerifyConfig struct {
	APIKey     string
	APISecret  string
	ServiceSID string
	Endpoint   string // Test-only override; production uses Twilio Verify v2 over HTTPS.
	HTTPClient *http.Client
}

type TwilioVerify struct {
	apiKey     string
	apiSecret  string
	serviceSID string
	endpoint   string
	client     *http.Client
}

type Verification struct {
	SID    string
	Status string
}

func NewTwilioVerify(cfg TwilioVerifyConfig) (*TwilioVerify, error) {
	if strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.APISecret) == "" || strings.TrimSpace(cfg.ServiceSID) == "" {
		return nil, ErrTwilioNotConfigured
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = twilioVerifyBaseURL + url.PathEscape(cfg.ServiceSID)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &TwilioVerify{apiKey: cfg.APIKey, apiSecret: cfg.APISecret, serviceSID: cfg.ServiceSID, endpoint: strings.TrimRight(endpoint, "/"), client: client}, nil
}

func (t *TwilioVerify) StartSMS(ctx context.Context, phone string) (Verification, error) {
	if !e164.MatchString(phone) {
		return Verification{}, ErrInvalidPhone
	}
	return t.request(ctx, "/Verifications", url.Values{"To": {phone}, "Channel": {"sms"}})
}

func (t *TwilioVerify) CheckSMS(ctx context.Context, phone, code string) (Verification, error) {
	if !e164.MatchString(phone) {
		return Verification{}, ErrInvalidPhone
	}
	if len(code) < 4 || len(code) > 10 || strings.TrimSpace(code) != code {
		return Verification{}, errors.New("invalid verification code")
	}
	return t.request(ctx, "/VerificationCheck", url.Values{"To": {phone}, "Code": {code}})
}

func (t *TwilioVerify) request(ctx context.Context, path string, form url.Values) (Verification, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint+path, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return Verification{}, err
	}
	req.SetBasicAuth(t.apiKey, t.apiSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := t.client.Do(req)
	if err != nil {
		return Verification{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Verification{}, fmt.Errorf("Twilio Verify rejected request with status %d", response.StatusCode)
	}
	var result struct {
		SID    string `json:"sid"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return Verification{}, err
	}
	if result.SID == "" || result.Status == "" {
		return Verification{}, errors.New("Twilio Verify returned an incomplete response")
	}
	return Verification{SID: result.SID, Status: result.Status}, nil
}

// Package notifications contains delivery adapters. They do not own business
// state; delivery requests originate from transactional outbox workers.
package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"
)

var ErrSendGridNotConfigured = errors.New("sendgrid is not configured")

const sendGridMailSendURL = "https://api.sendgrid.com/v3/mail/send"

type SendGridConfig struct {
	APIKey     string
	FromEmail  string
	TemplateID string
	Endpoint   string // Test-only override; production uses SendGrid's HTTPS endpoint.
	HTTPClient *http.Client
}

type SendGrid struct {
	apiKey    string
	fromEmail string
	endpoint  string
	client    *http.Client
}

func NewSendGrid(cfg SendGridConfig) (*SendGrid, error) {
	if strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.FromEmail) == "" {
		return nil, ErrSendGridNotConfigured
	}
	if _, err := mail.ParseAddress(cfg.FromEmail); err != nil {
		return nil, fmt.Errorf("invalid SendGrid sender: %w", err)
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = sendGridMailSendURL
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &SendGrid{apiKey: cfg.APIKey, fromEmail: cfg.FromEmail, endpoint: endpoint, client: client}, nil
}

func (s *SendGrid) SendEmailVerification(ctx context.Context, recipient, code string, expiresAt time.Time) error {
	subject := "Your Limiance verification code"
	plainText := fmt.Sprintf("Your Limiance verification code is %s. It expires in %d minutes. If you did not request this, you can ignore this email.", code, int(time.Until(expiresAt).Minutes()))
	html := fmt.Sprintf("<p>Your Limiance verification code is:</p><p style=\"font-size:28px;font-weight:700;letter-spacing:4px\">%s</p><p>It expires in %d minutes.</p><p>If you did not request this, you can ignore this email.</p>", code, int(time.Until(expiresAt).Minutes()))
	return s.send(ctx, recipient, subject, plainText, html)
}

// SendTestEmail validates the configured sender and delivery path without
// creating a user, OTP, or other financial/security state.
func (s *SendGrid) SendTestEmail(ctx context.Context, recipient string) error {
	return s.send(ctx, recipient, "Limiance email delivery test", "If you received this, Limiance email delivery is working.", "<p>If you received this, <strong>Limiance email delivery is working.</strong></p>")
}

func (s *SendGrid) send(ctx context.Context, recipient, subject, plainText, html string) error {
	if _, err := mail.ParseAddress(recipient); err != nil {
		return fmt.Errorf("invalid recipient: %w", err)
	}
	payload := struct {
		Personalizations []struct {
			To []map[string]string `json:"to"`
		} `json:"personalizations"`
		From    map[string]string `json:"from"`
		Subject string            `json:"subject"`
		Content []struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"content"`
	}{
		From:    map[string]string{"email": s.fromEmail},
		Subject: subject,
	}
	payload.Personalizations = append(payload.Personalizations, struct {
		To []map[string]string `json:"to"`
	}{
		To: []map[string]string{{"email": recipient}},
	})
	payload.Content = append(payload.Content, struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	}{Type: "text/plain", Value: plainText}, struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	}{Type: "text/html", Value: html})
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("SendGrid mail send rejected with status %d", response.StatusCode)
	}
	return nil
}

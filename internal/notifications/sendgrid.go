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

// TransactionalEmail is rendered by the notification worker after it has
// resolved an outbox event to a user. It intentionally contains no recipient
// identity; that remains an explicit argument to the delivery adapter.
type TransactionalEmail struct {
	Subject string
	Title   string
	Body    string
}

// TemplateForEvent returns the approved transactional copy for notification
// events. Keep this as an explicit allow-list so an arbitrary outbox payload
// can never become an email template.
func TemplateForEvent(eventType string) (TransactionalEmail, bool) {
	templates := map[string]TransactionalEmail{
		"deposit.submitted":         {"Deposit detected", "Deposit detected", "We detected your deposit and will update you as confirmations arrive."},
		"deposit.confirming":        {"Deposit confirming", "Your deposit is confirming", "Your deposit is awaiting the required blockchain confirmations."},
		"deposit.credited":          {"Deposit credited", "Your deposit is available", "Your deposit has been credited to your Funding account."},
		"deposit.failed":            {"Deposit update", "Your deposit could not be processed", "Your deposit was not credited. Contact support if you need assistance."},
		"withdrawal.submitted":      {"Withdrawal submitted", "Withdrawal submitted", "Your withdrawal request has been received and is awaiting review."},
		"withdrawal.under_review":   {"Withdrawal under review", "Withdrawal under review", "Your withdrawal requires additional review before it can be processed."},
		"withdrawal.approved":       {"Withdrawal approved", "Withdrawal approved", "Your withdrawal has passed approval and will be submitted for processing."},
		"withdrawal.completed":      {"Withdrawal completed", "Withdrawal completed", "Your withdrawal has been completed."},
		"withdrawal.rejected":       {"Withdrawal rejected", "Withdrawal rejected", "Your withdrawal was rejected. Contact support if you need assistance."},
		"transfer.completed":        {"Transfer completed", "Transfer completed", "Your transfer has been completed."},
		"security.new_device_login": {"New sign-in", "New sign-in to your Limiance account", "A new device signed in to your Limiance account. If this was not you, secure your account immediately."},
		"security.password_changed": {"Password changed", "Your password was changed", "Your Limiance password was changed. If this was not you, secure your account immediately."},
		"security.2fa_enabled":      {"Two-factor authentication enabled", "Two-factor authentication enabled", "Two-factor authentication is now enabled on your account."},
		"security.2fa_disabled":     {"Two-factor authentication disabled", "Two-factor authentication disabled", "Two-factor authentication was disabled on your account. If this was not you, secure your account immediately."},
		"security.api_key_created":  {"API key created", "A new API key was created", "A new API key was created for your account. If this was not you, revoke it immediately."},
	}
	template, ok := templates[eventType]
	return template, ok
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

func (s *SendGrid) SendPasswordReset(ctx context.Context, recipient, code string, expiresAt time.Time) error {
	subject := "Your Limiance password reset code"
	minutes := int(time.Until(expiresAt).Minutes())
	plainText := fmt.Sprintf("Your Limiance password reset code is %s. It expires in %d minutes. If you did not request this, secure your account immediately.", code, minutes)
	html := fmt.Sprintf("<p>Your Limiance password reset code is:</p><p style=\"font-size:28px;font-weight:700;letter-spacing:4px\">%s</p><p>It expires in %d minutes.</p><p>If you did not request this, secure your account immediately.</p>", code, minutes)
	return s.send(ctx, recipient, subject, plainText, html)
}

// SendTransactionalEmail delivers a code-owned template. HTML-escape all copy
// here as a defence-in-depth boundary before it reaches the email provider.
func (s *SendGrid) SendTransactionalEmail(ctx context.Context, recipient string, message TransactionalEmail) error {
	plainText := message.Title + "\n\n" + message.Body
	htmlBody := fmt.Sprintf("<h2>%s</h2><p>%s</p>", escapeHTML(message.Title), escapeHTML(message.Body))
	return s.send(ctx, recipient, message.Subject, plainText, htmlBody)
}

func escapeHTML(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	value = strings.ReplaceAll(value, `"`, "&quot;")
	return strings.ReplaceAll(value, "'", "&#39;")
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

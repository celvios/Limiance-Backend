package kyc

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrMissingSignature  = errors.New("missing sumsub webhook signature")
	ErrUnsupportedDigest = errors.New("unsupported sumsub webhook digest algorithm")
	ErrInvalidSignature  = errors.New("invalid sumsub webhook signature")
	ErrNotConfigured     = errors.New("Sumsub is not configured")
)

const sumsubBaseURL = "https://api.sumsub.com"

type ClientConfig struct {
	AppToken   string
	SecretKey  string
	LevelName  string
	Endpoint   string // Test-only override; production uses Sumsub HTTPS.
	HTTPClient *http.Client
}

type Client struct {
	appToken, secretKey, levelName, endpoint string
	client                                   *http.Client
}

type Applicant struct{ ID string }

func NewClient(cfg ClientConfig) (*Client, error) {
	if strings.TrimSpace(cfg.AppToken) == "" || strings.TrimSpace(cfg.SecretKey) == "" || strings.TrimSpace(cfg.LevelName) == "" {
		return nil, ErrNotConfigured
	}
	endpoint := strings.TrimRight(cfg.Endpoint, "/")
	if endpoint == "" {
		endpoint = sumsubBaseURL
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{appToken: cfg.AppToken, secretKey: cfg.SecretKey, levelName: cfg.LevelName, endpoint: endpoint, client: client}, nil
}

func (c *Client) CreateApplicant(ctx context.Context, externalUserID, email string) (Applicant, error) {
	query := url.Values{"levelName": {c.levelName}}.Encode()
	body, err := json.Marshal(map[string]string{"externalUserId": externalUserID, "email": email})
	if err != nil {
		return Applicant{}, err
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/resources/applicants?"+query, body, &result); err != nil {
		return Applicant{}, err
	}
	if result.ID == "" {
		return Applicant{}, errors.New("Sumsub returned an applicant without an ID")
	}
	return Applicant{ID: result.ID}, nil
}

func (c *Client) CreateAccessToken(ctx context.Context, externalUserID, email string) (string, error) {
	body, err := json.Marshal(map[string]any{"userId": externalUserID, "levelName": c.levelName, "ttlInSecs": 600, "applicantIdentifiers": map[string]string{"email": email}})
	if err != nil {
		return "", err
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/resources/accessTokens/sdk", body, &result); err != nil {
		return "", err
	}
	if result.Token == "" {
		return "", errors.New("Sumsub returned an empty SDK token")
	}
	return result.Token, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, body []byte, target any) error {
	ts := fmt.Sprintf("%d", time.Now().Unix())
	mac := hmac.New(sha256.New, []byte(c.secretKey))
	_, _ = mac.Write(append([]byte(ts+method+path), body...))
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-App-Token", c.appToken)
	req.Header.Set("X-App-Access-Ts", ts)
	req.Header.Set("X-App-Access-Sig", hex.EncodeToString(mac.Sum(nil)))
	response, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Sumsub rejected request with status %d", response.StatusCode)
	}
	return json.NewDecoder(response.Body).Decode(target)
}

// VerifyWebhook checks Sumsub's x-payload-digest against the unmodified request bytes.
// It must run before JSON decoding or persistence.
func VerifyWebhook(secret, algorithm, signature string, raw []byte) error {
	if secret == "" || signature == "" {
		return ErrMissingSignature
	}
	var digest []byte
	switch strings.ToUpper(algorithm) {
	case "HMAC_SHA256_HEX", "":
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(raw)
		digest = mac.Sum(nil)
	case "HMAC_SHA512_HEX":
		mac := hmac.New(sha512.New, []byte(secret))
		_, _ = mac.Write(raw)
		digest = mac.Sum(nil)
	default:
		return ErrUnsupportedDigest
	}
	received, err := hex.DecodeString(signature)
	if err != nil || subtle.ConstantTimeCompare(digest, received) != 1 {
		return ErrInvalidSignature
	}
	return nil
}

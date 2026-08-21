package custody

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	ErrMissingWebhookSignature = errors.New("missing Fireblocks webhook signature")
	ErrInvalidWebhookSignature = errors.New("invalid Fireblocks webhook signature")
)

// WebhookVerifier validates Fireblocks Webhooks V2's detached JWS with the
// provider's rotating JWKS. It uses the EU JWKS URL for an EU workspace.
type WebhookVerifier struct {
	url    string
	client *http.Client
	mu     sync.Mutex
	keys   map[string]*rsa.PublicKey
	expiry time.Time
}

func NewWebhookVerifier(jwksURL string, client *http.Client) (*WebhookVerifier, error) {
	if !strings.HasPrefix(jwksURL, "https://") {
		return nil, errors.New("Fireblocks JWKS URL must use HTTPS")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &WebhookVerifier{url: jwksURL, client: client, keys: make(map[string]*rsa.PublicKey)}, nil
}

func (v *WebhookVerifier) Verify(ctx context.Context, rawBody []byte, detachedJWS string) error {
	parts := strings.Split(detachedJWS, ".")
	if detachedJWS == "" {
		return ErrMissingWebhookSignature
	}
	if len(parts) != 3 || parts[0] == "" || parts[1] != "" || parts[2] == "" {
		return ErrInvalidWebhookSignature
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ErrInvalidWebhookSignature
	}
	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	if json.Unmarshal(headerBytes, &header) != nil || header.Algorithm != "RS512" || header.KeyID == "" {
		return ErrInvalidWebhookSignature
	}
	key, err := v.key(ctx, header.KeyID)
	if err != nil {
		return err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return ErrInvalidWebhookSignature
	}
	payload := base64.RawURLEncoding.EncodeToString(rawBody)
	hash := sha512.Sum512([]byte(parts[0] + "." + payload))
	if rsa.VerifyPKCS1v15(key, crypto.SHA512, hash[:], signature) != nil {
		return ErrInvalidWebhookSignature
	}
	return nil
}

func (v *WebhookVerifier) key(ctx context.Context, keyID string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if time.Now().Before(v.expiry) {
		if key := v.keys[keyID]; key != nil {
			return key, nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.url, nil)
	if err != nil {
		return nil, err
	}
	response, err := v.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("Fireblocks JWKS request failed")
	}
	var document struct {
		Keys []struct {
			KeyType string `json:"kty"`
			KeyID   string `json:"kid"`
			Use     string `json:"use"`
			Alg     string `json:"alg"`
			N       string `json:"n"`
			E       string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&document); err != nil {
		return nil, err
	}
	fresh := make(map[string]*rsa.PublicKey, len(document.Keys))
	for _, candidate := range document.Keys {
		if candidate.KeyType != "RSA" || candidate.Use != "sig" || candidate.Alg != "RS512" || candidate.KeyID == "" {
			continue
		}
		modulus, err := base64.RawURLEncoding.DecodeString(candidate.N)
		if err != nil {
			continue
		}
		exponent, err := base64.RawURLEncoding.DecodeString(candidate.E)
		if err != nil || len(exponent) == 0 {
			continue
		}
		e := 0
		for _, value := range exponent {
			e = e<<8 | int(value)
		}
		if e < 3 {
			continue
		}
		fresh[candidate.KeyID] = &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: e}
	}
	if len(fresh) == 0 {
		return nil, errors.New("Fireblocks JWKS did not contain usable signing keys")
	}
	v.keys = fresh
	v.expiry = time.Now().Add(time.Hour)
	key := v.keys[keyID]
	if key == nil {
		return nil, ErrInvalidWebhookSignature
	}
	return key, nil
}

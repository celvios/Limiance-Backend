package custody

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultFireblocksTimeout = 15 * time.Second

// FireblocksConfig is deliberately limited to the credentials and endpoint the
// backend needs. The private key is injected from a secret manager at runtime.
type FireblocksConfig struct {
	APIKey     string
	PrivateKey string
	BaseURL    string // e.g. https://sandbox-api.fireblocks.io
	HTTPClient *http.Client
	Now        func() time.Time
}

// FireblocksClient is the real Phase 1 MPC-custody adapter. It uses the
// documented Fireblocks REST/JWT protocol directly so custody is not coupled
// to an unofficial Go SDK.
type FireblocksClient struct {
	apiKey  string
	key     *rsa.PrivateKey
	baseURL *url.URL
	client  *http.Client
	now     func() time.Time
}

// SupportedAsset is the safe subset of Fireblocks' asset metadata required to
// map a Limiance asset/network route. Fireblocks remains authoritative for the
// provider asset ID and the testnet/mainnet distinction.
type SupportedAsset struct {
	ID            string `json:"id"`
	LegacyID      string `json:"legacyId"`
	DisplayName   string `json:"displayName"`
	DisplaySymbol string `json:"displaySymbol"`
	BlockchainID  string `json:"blockchainId"`
	AssetClass    string `json:"assetClass"`
	Onchain       struct {
		Symbol   string `json:"symbol"`
		Name     string `json:"name"`
		Decimals int    `json:"decimals"`
	} `json:"onchain"`
}

var _ Provider = (*FireblocksClient)(nil)

func (*FireblocksClient) ProviderID() string { return "fireblocks" }

func NewFireblocksClient(cfg FireblocksConfig) (*FireblocksClient, error) {
	if strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.PrivateKey) == "" || strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("fireblocks credentials and base URL are required")
	}
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"))
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, errors.New("fireblocks base URL must be an HTTPS URL")
	}
	key, err := parseRSAPrivateKey([]byte(cfg.PrivateKey))
	if err != nil {
		return nil, err
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultFireblocksTimeout}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &FireblocksClient{apiKey: strings.TrimSpace(cfg.APIKey), key: key, baseURL: base, client: client, now: now}, nil
}

func (c *FireblocksClient) CreateCustomerWallet(ctx context.Context, customerReference string) (Wallet, error) {
	ref := strings.TrimSpace(customerReference)
	if ref == "" || !isASCII(ref) {
		return Wallet{}, errors.New("fireblocks customer reference must be non-empty ASCII")
	}
	body, err := json.Marshal(map[string]any{
		"name":          "limiance-" + ref,
		"hiddenOnUI":    true,
		"customerRefId": ref,
		"vaultType":     "MPC",
	})
	if err != nil {
		return Wallet{}, err
	}
	var response struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/vault/accounts", body, DeterministicIdempotencyKey("wallet", ref), &response); err != nil {
		return Wallet{}, err
	}
	if response.ID == "" {
		return Wallet{}, errors.New("fireblocks vault response did not contain an ID")
	}
	return Wallet{ID: response.ID, Status: "created"}, nil
}

// GetDepositAddress creates the asset wallet for this customer vault. This is
// the correct Fireblocks flow for account-based assets; dedicated address
// creation is only for UTXO and tag/memo-based assets.
func (c *FireblocksClient) GetDepositAddress(ctx context.Context, walletID, assetID, idempotencyKey string) (DepositAddress, error) {
	if strings.TrimSpace(walletID) == "" || strings.TrimSpace(assetID) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return DepositAddress{}, errors.New("fireblocks wallet ID, asset ID, and idempotency key are required")
	}
	path := "/v1/vault/accounts/" + url.PathEscape(walletID) + "/" + url.PathEscape(assetID)
	var response struct {
		ID      string `json:"id"`
		Address string `json:"address"`
		Tag     string `json:"tag"`
		Status  string `json:"status"`
	}
	if err := c.do(ctx, http.MethodPost, path, []byte("{}"), idempotencyKey, &response); err != nil {
		return DepositAddress{}, err
	}
	if response.Address == "" {
		return DepositAddress{}, errors.New("fireblocks asset wallet is not ready for deposits")
	}
	return DepositAddress{ID: response.ID, Address: response.Address, Tag: response.Tag}, nil
}

func (c *FireblocksClient) CreateWithdrawal(ctx context.Context, input WithdrawalRequest) (Withdrawal, error) {
	if input.SourceVaultID == "" || input.AssetID == "" || input.Destination == "" || input.Amount == "" || input.ExternalID == "" {
		return Withdrawal{}, errors.New("fireblocks withdrawal fields are required")
	}
	body, err := json.Marshal(map[string]any{
		"assetId":      input.AssetID,
		"amount":       input.Amount,
		"source":       map[string]string{"type": "VAULT_ACCOUNT", "id": input.SourceVaultID},
		"destination":  map[string]any{"type": "ONE_TIME_ADDRESS", "oneTimeAddress": map[string]string{"address": input.Destination, "tag": input.DestinationTag}},
		"externalTxId": input.ExternalID,
	})
	if err != nil {
		return Withdrawal{}, err
	}
	var response struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/transactions", body, DeterministicIdempotencyKey("withdrawal", input.ExternalID), &response); err != nil {
		return Withdrawal{}, err
	}
	if response.ID == "" {
		return Withdrawal{}, errors.New("fireblocks withdrawal response did not contain a transaction ID")
	}
	return Withdrawal{ProviderTransactionID: response.ID, Status: response.Status}, nil
}

func AtomicToProviderAmount(amount string, decimals int16) (string, error) {
	if decimals < 0 || amount == "" {
		return "", errors.New("invalid atomic amount")
	}
	value, ok := new(big.Int).SetString(amount, 10)
	if !ok || value.Sign() <= 0 {
		return "", errors.New("invalid atomic amount")
	}
	base := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	whole := new(big.Int).Quo(value, base)
	fraction := new(big.Int).Mod(value, base)
	if fraction.Sign() == 0 {
		return whole.String(), nil
	}
	return whole.String() + "." + strings.TrimRight(fmt.Sprintf("%0*s", int(decimals), fraction.String()), "0"), nil
}

// ListSupportedAssets reads the provider's workspace-specific asset registry.
// It does not create wallets, addresses, transactions, or local state.
func (c *FireblocksClient) ListSupportedAssets(ctx context.Context) ([]SupportedAsset, error) {
	var response struct {
		Data []SupportedAsset `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/assets?pageSize=1000", nil, "", &response); err != nil {
		return nil, err
	}
	return response.Data, nil
}

func (c *FireblocksClient) do(ctx context.Context, method, path string, body []byte, idempotencyKey string, output any) error {
	jwt, err := c.signedJWT(method, path, body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL.String()+path, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		limited, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("fireblocks API returned %d: %s", response.StatusCode, strings.TrimSpace(string(limited)))
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output)
}

func (c *FireblocksClient) signedJWT(_ string, path string, body []byte) (string, error) {
	now := c.now().UTC()
	nonce, err := randomNonce()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	payload, err := json.Marshal(map[string]any{
		"uri":      path,
		"nonce":    nonce,
		"iat":      now.Unix(),
		"exp":      now.Add(29 * time.Second).Unix(),
		"sub":      c.apiKey,
		"bodyHash": hex.EncodeToString(digest[:]),
	})
	if err != nil {
		return "", err
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := base64.RawURLEncoding.EncodeToString(payload)
	signingInput := header + "." + claims
	hash := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, hash[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func parseRSAPrivateKey(value []byte) (*rsa.PrivateKey, error) {
	value = []byte(strings.ReplaceAll(string(value), `\n`, "\n"))
	block, _ := pem.Decode(value)
	if block == nil {
		return nil, errors.New("fireblocks private key is not PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("fireblocks private key is not a supported RSA key")
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("fireblocks private key is not RSA")
	}
	return rsaKey, nil
}

// PublicKeyFingerprint returns a non-sensitive SHA-256 fingerprint of the
// public key corresponding to an RSA private key PEM. It is intended for
// checking that a configured API secret matches the CSR registered with a
// Fireblocks API user; it never returns private-key material.
func PublicKeyFingerprint(value []byte) (string, error) {
	key, err := parseRSAPrivateKey(value)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(der)
	return hex.EncodeToString(digest[:]), nil
}

// CSRPublicKeyFingerprint returns the same public-key fingerprint for a CSR
// PEM. A matching value means the CSR was created from the private key.
func CSRPublicKeyFingerprint(value []byte) (string, error) {
	block, _ := pem.Decode(value)
	if block == nil {
		return "", errors.New("fireblocks CSR is not PEM")
	}
	request, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return "", errors.New("fireblocks CSR is invalid")
	}
	der, err := x509.MarshalPKIXPublicKey(request.PublicKey)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(der)
	return hex.EncodeToString(digest[:]), nil
}

// DeterministicIdempotencyKey produces a stable provider-safe key for a single
// logical custody action. Fireblocks accepts a maximum 40-character key, while
// UUID-based customer and asset identifiers can be longer than that.
func DeterministicIdempotencyKey(prefix string, values ...string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "op-"
	}
	if !strings.HasSuffix(prefix, "-") {
		prefix += "-"
	}
	digest := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	encoded := hex.EncodeToString(digest[:])
	remaining := 40 - len(prefix)
	if remaining <= 0 {
		return encoded[:40]
	}
	if remaining > len(encoded) {
		remaining = len(encoded)
	}
	return prefix + encoded[:remaining]
}

func randomNonce() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func isASCII(value string) bool {
	for _, r := range value {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}

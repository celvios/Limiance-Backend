package custody

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SelfCustodyTestnetClient connects only to the isolated Limiance signer
// service. It has no private-key or KMS permission and is deliberately limited
// to address issuance at this stage; future transaction signing remains an
// independently authorised worker-only operation.
type SelfCustodyTestnetClient struct {
	baseURL *url.URL
	client  *http.Client
}

var _ Provider = (*SelfCustodyTestnetClient)(nil)

func (*SelfCustodyTestnetClient) ProviderID() string { return "limiance_self_custody_testnet" }

func NewSelfCustodyTestnetClient(rawURL string, client *http.Client) (*SelfCustodyTestnetClient, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(rawURL), "/"))
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, errors.New("self-custody signer URL must be HTTPS")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &SelfCustodyTestnetClient{baseURL: base, client: client}, nil
}

func (c *SelfCustodyTestnetClient) CreateCustomerWallet(ctx context.Context, customerReference string) (Wallet, error) {
	var response struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/testnet/wallets", map[string]string{"customer_reference": customerReference}, &response); err != nil {
		return Wallet{}, err
	}
	if strings.TrimSpace(response.ID) == "" {
		return Wallet{}, errors.New("self-custody signer did not return a wallet ID")
	}
	return Wallet{ID: response.ID, Status: "active"}, nil
}

func (c *SelfCustodyTestnetClient) GetDepositAddress(ctx context.Context, walletID, assetID, idempotencyKey string) (DepositAddress, error) {
	if walletID == "" || assetID == "" || idempotencyKey == "" {
		return DepositAddress{}, errors.New("wallet, asset, and idempotency key are required")
	}
	var response struct {
		ID      string `json:"id"`
		Address string `json:"address"`
		Tag     string `json:"tag"`
	}
	path := "/v1/testnet/wallets/" + url.PathEscape(walletID) + "/addresses"
	if err := c.do(ctx, http.MethodPost, path, map[string]string{"asset_id": assetID, "idempotency_key": idempotencyKey}, &response); err != nil {
		return DepositAddress{}, err
	}
	if strings.TrimSpace(response.Address) == "" {
		return DepositAddress{}, errors.New("self-custody signer did not return an address")
	}
	return DepositAddress{ID: response.ID, Address: response.Address, Tag: response.Tag}, nil
}

func (c *SelfCustodyTestnetClient) do(ctx context.Context, method, path string, input any, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL.String()+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("self-custody signer returned %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output)
}

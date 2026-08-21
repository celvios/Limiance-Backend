// custody-preflight verifies Fireblocks API authentication and prints only
// non-sensitive, workspace-authoritative test-asset metadata. It is read-only.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/custody"
)

func main() {
	cfg := config.Load()
	client, err := custody.NewFireblocksClient(custody.FireblocksConfig{
		APIKey:     cfg.FireblocksAPIKey,
		PrivateKey: cfg.FireblocksPrivateKey,
		BaseURL:    cfg.FireblocksBaseURL,
	})
	if err != nil {
		fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	assets, err := client.ListSupportedAssets(ctx)
	if err != nil {
		fatal(err)
	}
	selected := make([]custody.SupportedAsset, 0)
	for _, asset := range assets {
		if relatedToDepositPilot(asset) {
			selected = append(selected, asset)
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(selected); err != nil {
		fatal(err)
	}
}

func relatedToDepositPilot(asset custody.SupportedAsset) bool {
	fields := []string{asset.LegacyID, asset.DisplayName, asset.DisplaySymbol, asset.Onchain.Symbol, asset.Onchain.Name}
	for _, field := range fields {
		value := strings.ToUpper(field)
		if strings.Contains(value, "BTC") || strings.Contains(value, "BITCOIN") || strings.Contains(value, "ETH") || strings.Contains(value, "ETHEREUM") {
			return true
		}
	}
	return false
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "Fireblocks custody preflight failed:", err)
	os.Exit(1)
}

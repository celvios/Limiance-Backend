// custody-preflight verifies Fireblocks API authentication and prints only
// non-sensitive, workspace-authoritative test-asset metadata. It is read-only.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/custody"
)

func main() {
	symbols := flag.String("symbols", "BTC,ETH", "comma-separated asset symbols or provider IDs to print")
	ndjson := flag.Bool("ndjson", false, "print one JSON asset record per line")
	flag.Parse()
	requested := make(map[string]struct{})
	for _, symbol := range strings.Split(*symbols, ",") {
		if value := strings.ToUpper(strings.TrimSpace(symbol)); value != "" {
			requested[value] = struct{}{}
		}
	}
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
		if relatedToDepositPilot(asset, requested) {
			selected = append(selected, asset)
		}
	}
	encoder := json.NewEncoder(os.Stdout)
	if *ndjson {
		for _, asset := range selected {
			if err := encoder.Encode(asset); err != nil {
				fatal(err)
			}
		}
		return
	}
	if err := encoder.Encode(selected); err != nil {
		fatal(err)
	}
}

func relatedToDepositPilot(asset custody.SupportedAsset, requested map[string]struct{}) bool {
	fields := []string{asset.LegacyID, asset.DisplayName, asset.DisplaySymbol, asset.Onchain.Symbol, asset.Onchain.Name}
	for _, field := range fields {
		value := strings.ToUpper(field)
		for symbol := range requested {
			if strings.Contains(value, symbol) {
				return true
			}
		}
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

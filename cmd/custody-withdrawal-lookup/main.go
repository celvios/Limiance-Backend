// custody-withdrawal-lookup performs read-only provider lookups by the stable
// Limiance withdrawal UUID. It never creates, retries, or mutates a transaction.
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
	raw := flag.String("external-ids", "", "comma-separated Limiance withdrawal UUIDs")
	flag.Parse()
	ids, err := parseExternalIDs(*raw)
	if err != nil {
		fatal(err)
	}
	cfg := config.Load()
	client, err := custody.NewFireblocksClient(custody.FireblocksConfig{
		APIKey: cfg.FireblocksAPIKey, PrivateKey: cfg.FireblocksPrivateKey, BaseURL: cfg.FireblocksBaseURL,
	})
	if err != nil {
		fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(len(ids))*20*time.Second)
	defer cancel()
	encoder := json.NewEncoder(os.Stdout)
	for _, id := range ids {
		result, err := client.FindWithdrawalByExternalID(ctx, id)
		if err != nil {
			fatal(fmt.Errorf("lookup %s: %w", id, err))
		}
		if err := encoder.Encode(result); err != nil {
			fatal(err)
		}
	}
}

func parseExternalIDs(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	if len(parts) > 50 {
		return nil, fmt.Errorf("at most 50 external IDs are allowed")
	}
	ids := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("at least one external ID is required")
	}
	return ids, nil
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "custody withdrawal lookup failed:", err)
	os.Exit(1)
}

// reconcile-staging-withdrawals repairs stale staging ledger state using a
// fresh read-only provider lookup. It cannot create or retry a withdrawal.
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
	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/platform/database"
)

type result struct {
	WithdrawalID          string `json:"withdrawal_id"`
	ProviderTransactionID string `json:"provider_transaction_id"`
	ProviderStatus        string `json:"provider_status"`
	Changed               bool   `json:"changed"`
}

func main() {
	raw := flag.String("external-ids", "", "comma-separated Limiance withdrawal UUIDs")
	confirm := flag.Bool("confirm-staging", false, "required staging-only safety acknowledgement")
	flag.Parse()
	ids, err := parseIDs(*raw)
	if err != nil {
		fatal(err)
	}
	cfg := config.Load()
	if !*confirm || cfg.Environment != "staging" {
		fatal(fmt.Errorf("staging environment and --confirm-staging are required"))
	}
	client, err := custody.NewFireblocksClient(custody.FireblocksConfig{
		APIKey: cfg.FireblocksAPIKey, PrivateKey: cfg.FireblocksPrivateKey, BaseURL: cfg.FireblocksBaseURL,
	})
	if err != nil {
		fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(len(ids))*30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal(err)
	}
	defer pool.Close()
	manager := datamanager.New(pool)
	encoder := json.NewEncoder(os.Stdout)
	for _, id := range ids {
		lookup, err := client.FindWithdrawalByExternalID(ctx, id)
		if err != nil {
			fatal(fmt.Errorf("provider lookup %s: %w", id, err))
		}
		status := strings.ToUpper(strings.TrimSpace(lookup.Status))
		if !lookup.Found || lookup.ExternalID != id || lookup.ProviderTransactionID == "" || !terminal(status) {
			fatal(fmt.Errorf("withdrawal %s lacks an explicit terminal provider outcome", id))
		}
		if err := manager.MarkWithdrawalSubmitted(ctx, id, lookup.ProviderTransactionID); err != nil {
			fatal(fmt.Errorf("bind provider identity %s: %w", id, err))
		}
		changed, err := manager.RecordWithdrawalCustodyUpdate(ctx, datamanager.WithdrawalCustodyUpdate{
			ProviderTransactionID: lookup.ProviderTransactionID, Status: status, TransactionHash: lookup.TransactionHash,
		})
		if err != nil {
			fatal(fmt.Errorf("apply provider outcome %s: %w", id, err))
		}
		if err := encoder.Encode(result{WithdrawalID: id, ProviderTransactionID: lookup.ProviderTransactionID, ProviderStatus: status, Changed: changed}); err != nil {
			fatal(err)
		}
	}
}

func parseIDs(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	if len(parts) > 50 {
		return nil, fmt.Errorf("at most 50 withdrawal IDs are allowed")
	}
	ids := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("at least one withdrawal ID is required")
	}
	return ids, nil
}

func terminal(status string) bool {
	switch status {
	case "COMPLETED", "CONFIRMED", "FAILED", "REJECTED", "CANCELLED":
		return true
	default:
		return false
	}
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "staging withdrawal reconciliation failed:", err)
	os.Exit(1)
}

package main

import (
	"context"
	"log"
	"time"

	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/custody"
	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/platform/database"
	"github.com/limiance/backend/internal/withdrawals"
)

func main() {
	cfg := config.Load()
	ctx := context.Background()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	var provider custody.Provider
	switch cfg.CustodyMode {
	case "self_custody_testnet":
		provider, err = custody.NewSelfCustodyTestnetClient(cfg.SelfCustodySignerURL, nil)
	default:
		provider, err = custody.NewFireblocksClient(custody.FireblocksConfig{APIKey: cfg.FireblocksAPIKey, PrivateKey: cfg.FireblocksPrivateKey, BaseURL: cfg.FireblocksBaseURL})
	}
	if err != nil {
		log.Fatal(err)
	}
	policy := custody.RoutePolicy{Mode: cfg.CustodyMode, SelfCustodyTestnetEnabled: cfg.SelfCustodyTestnetEnabled, TestnetOnly: cfg.Environment == "staging"}
	worker := withdrawals.NewWorker(datamanager.New(pool), provider, provider.ProviderID(), policy)
	for {
		if _, err := worker.RunOnce(ctx); err != nil {
			log.Printf("withdrawal worker failed: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		time.Sleep(2 * time.Second)
	}
}

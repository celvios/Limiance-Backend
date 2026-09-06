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
	policy := custody.RoutePolicy{Environment: cfg.Environment, Mode: cfg.CustodyMode, SelfCustodyTestnetEnabled: cfg.SelfCustodyTestnetEnabled, TestnetOnly: cfg.Environment == "staging"}
	data := datamanager.New(pool)
	worker := withdrawals.NewWorker(data, provider, provider.ProviderID(), policy)
	var reconciler *withdrawals.Reconciler
	if observer, ok := provider.(custody.WithdrawalObserver); ok {
		reconciler = withdrawals.NewReconciler(data, observer, provider.ProviderID())
	}
	for {
		if reconciler != nil {
			if _, err := reconciler.RunOnce(ctx); err != nil {
				log.Printf("withdrawal reconciliation failed: %v", err)
				time.Sleep(5 * time.Second)
				continue
			}
		}
		if _, err := worker.RunOnce(ctx); err != nil {
			log.Printf("withdrawal worker failed: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		time.Sleep(2 * time.Second)
	}
}

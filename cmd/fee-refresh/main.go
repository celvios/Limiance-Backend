package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/platform/database"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg := config.Load()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal(err)
	}
	defer pool.Close()
	if err := datamanager.New(pool).RefreshUserFeeTiers(ctx); err != nil {
		fatal(err)
	}
	fmt.Println("fee tiers refreshed")
}

func fatal(err error) {
	fmt.Println(err)
	os.Exit(1)
}

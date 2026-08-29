package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/platform/database"
	"github.com/limiance/backend/internal/trading"
	tradingpostgres "github.com/limiance/backend/internal/trading/postgres"
	"github.com/limiance/backend/internal/trading/transport"
)

func main() {
	cfg := config.Load()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("settlement database connection failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	subscriber, err := transport.NewSubscriber(ctx, cfg.MatchingEngineEventsURL, cfg.MatchingEngineTimeout, transport.TopicTrade)
	if err != nil {
		logger.Error("trade event subscription failed", "error", err)
		os.Exit(1)
	}
	defer subscriber.Close()
	control, err := transport.NewRequestClient(ctx, cfg.MatchingEngineControlURL, cfg.MatchingEngineTimeout)
	if err != nil {
		logger.Error("matching engine replay control failed", "error", err)
		os.Exit(1)
	}
	defer control.Close()
	consumer := trading.NewSettlementConsumer(tradingpostgres.New(pool), trading.NewControlReplayRequester(control))
	logger.Info("trade settlement consumer started", "events_endpoint", cfg.MatchingEngineEventsURL)
	if err = consumer.Run(ctx, subscriber); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("trade settlement consumer stopped", "error", err)
		os.Exit(1)
	}
	logger.Info("trade settlement consumer stopped")
}

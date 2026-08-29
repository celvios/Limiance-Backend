package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/marketdata"
	"github.com/limiance/backend/internal/platform/database"
	"github.com/limiance/backend/internal/trading"
	"github.com/limiance/backend/internal/trading/transport"
)

func main() {
	cfg := config.Load()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if cfg.RedisURL == "" {
		logger.Error("market data requires REDIS_URL")
		os.Exit(1)
	}
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("market database connection failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	cache, err := marketdata.NewRedisStore(cfg.RedisURL)
	if err != nil {
		logger.Error("market Redis connection failed", "error", err)
		os.Exit(1)
	}
	defer cache.Close()
	subscriber, err := transport.NewSubscriber(ctx, cfg.MatchingEngineEventsURL, cfg.MatchingEngineTimeout, transport.TopicTrade, transport.TopicOrderBook)
	if err != nil {
		logger.Error("market event subscription failed", "error", err)
		os.Exit(1)
	}
	defer subscriber.Close()
	control, err := transport.NewRequestClient(ctx, cfg.MatchingEngineControlURL, cfg.MatchingEngineTimeout)
	if err != nil {
		logger.Error("market replay control failed", "error", err)
		os.Exit(1)
	}
	defer control.Close()
	consumer := marketdata.NewConsumer(marketdata.NewPostgresStore(pool), cache, trading.NewControlReplayRequester(control))
	logger.Info("market data consumer started", "events_endpoint", cfg.MatchingEngineEventsURL)
	if err = consumer.Run(ctx, subscriber); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("market data consumer stopped", "error", err)
		os.Exit(1)
	}
}

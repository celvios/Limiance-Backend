package pnl

import (
	"context"
	"time"
)

type DailyPnLEntry struct {
	Date   time.Time `json:"date"`
	PNLUSD string    `json:"pnl_usd"`
}

type Repository interface {
	DailyPnL(ctx context.Context, userID string, days int) ([]DailyPnLEntry, error)
}

type Service struct {
	repository Repository
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository}
}

func (s *Service) DailyPnL(ctx context.Context, userID string, days int) ([]DailyPnLEntry, error) {
	if days <= 0 {
		days = 30
	}
	return s.repository.DailyPnL(ctx, userID, days)
}

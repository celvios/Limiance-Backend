package pnl

import (
	"context"
	"testing"
	"time"
)

type dailyPnlRepositoryStub struct {
	items []DailyPnLEntry
	reads int
}

func (s *dailyPnlRepositoryStub) DailyPnL(_ context.Context, userID string, days int) ([]DailyPnLEntry, error) {
	s.reads++
	if userID == "" {
		return nil, nil
	}
	if days <= 0 {
		days = 30
	}
	return s.items[:min(len(s.items), days)], nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestServiceDailyPnLUsesDefaultWindow(t *testing.T) {
	repo := &dailyPnlRepositoryStub{items: []DailyPnLEntry{{Date: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), PNLUSD: "120.00"}, {Date: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), PNLUSD: "-40.00"}}}
	service := NewService(repo)

	got, err := service.DailyPnL(context.Background(), "user-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if repo.reads != 1 {
		t.Fatalf("expected one repo read, got %d", repo.reads)
	}
}

func TestServiceDailyPnLRespectsRequestedWindow(t *testing.T) {
	repo := &dailyPnlRepositoryStub{items: []DailyPnLEntry{{Date: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), PNLUSD: "10.00"}, {Date: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), PNLUSD: "20.00"}, {Date: time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC), PNLUSD: "30.00"}}}
	service := NewService(repo)

	got, err := service.DailyPnL(context.Background(), "user-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
}

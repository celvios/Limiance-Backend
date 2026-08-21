package deposits

import (
	"context"
	"github.com/limiance/backend/internal/datamanager"
)

type HistoryService struct{ data *datamanager.Manager }

func NewHistoryService(data *datamanager.Manager) *HistoryService { return &HistoryService{data: data} }
func (s *HistoryService) List(ctx context.Context, userID string, limit int) ([]datamanager.DepositHistoryItem, error) {
	return s.data.DepositHistory(ctx, userID, limit)
}

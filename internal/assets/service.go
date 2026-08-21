package assets

import (
	"context"

	"github.com/limiance/backend/internal/datamanager"
)

type CatalogItem struct {
	Code        string `json:"code"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
}

type Catalog struct {
	Assets   []CatalogItem `json:"assets"`
	Networks []CatalogItem `json:"networks"`
}

type Service struct{ data *datamanager.Manager }

func NewService(data *datamanager.Manager) *Service { return &Service{data: data} }

func (s *Service) Catalog(ctx context.Context) (Catalog, error) {
	assets, err := s.data.AssetCatalog(ctx)
	if err != nil {
		return Catalog{}, err
	}
	networks, err := s.data.NetworkCatalog(ctx)
	if err != nil {
		return Catalog{}, err
	}

	result := Catalog{
		Assets:   make([]CatalogItem, 0, len(assets)),
		Networks: make([]CatalogItem, 0, len(networks)),
	}
	for _, asset := range assets {
		result.Assets = append(result.Assets, CatalogItem{Code: asset.Symbol, DisplayName: asset.DisplayName, Status: asset.Status})
	}
	for _, network := range networks {
		result.Networks = append(result.Networks, CatalogItem{Code: network.Code, DisplayName: network.DisplayName, Status: network.Status})
	}
	return result, nil
}

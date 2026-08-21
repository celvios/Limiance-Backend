// Package deposits owns Deposit Gateway interpretation and policy transitions.
package deposits

import (
	"encoding/json"
	"errors"
	"math/big"
	"strings"
)

var ErrNotDepositEvent = errors.New("not a usable Fireblocks deposit event")

type ChainEvent struct {
	ProviderTransactionID string
	AssetID               string
	DestinationAddress    string
	DestinationTag        string
	Status                string
	TransactionHash       string
	BlockchainIndex       string
	BlockHash             string
	BlockHeight           string
	Confirmations         int
	Amount                string
}

// ParseFireblocksEvent accepts Webhooks V2 transaction creation/status events.
// It intentionally ignores outgoing, multi-destination, non-transfer, and
// incomplete payloads; those require a different workflow or manual review.
func ParseFireblocksEvent(raw []byte) (ChainEvent, error) {
	var payload struct {
		EventType string `json:"eventType"`
		Data      struct {
			ID                 string `json:"id"`
			Status             string `json:"status"`
			Operation          string `json:"operation"`
			AssetID            string `json:"assetId"`
			DestinationAddress string `json:"destinationAddress"`
			DestinationTag     string `json:"destinationTag"`
			TxHash             string `json:"txHash"`
			BlockchainIndex    string `json:"blockchainIndex"`
			Confirmations      int    `json:"numOfConfirmations"`
			BlockInfo          struct {
				BlockHash   string `json:"blockHash"`
				BlockHeight string `json:"blockHeight"`
			} `json:"blockInfo"`
			AmountInfo struct {
				Amount    string `json:"amount"`
				NetAmount string `json:"netAmount"`
			} `json:"amountInfo"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ChainEvent{}, ErrNotDepositEvent
	}
	if payload.EventType != "transaction.created" && payload.EventType != "transaction.status.updated" {
		return ChainEvent{}, ErrNotDepositEvent
	}
	if payload.Data.Operation != "TRANSFER" || payload.Data.ID == "" || payload.Data.AssetID == "" || payload.Data.DestinationAddress == "" || payload.Data.Status == "" || payload.Data.Confirmations < 0 {
		return ChainEvent{}, ErrNotDepositEvent
	}
	amount := payload.Data.AmountInfo.NetAmount
	if amount == "" {
		amount = payload.Data.AmountInfo.Amount
	}
	parsedAmount, ok := new(big.Rat).SetString(amount)
	if !ok || parsedAmount.Sign() <= 0 || strings.HasPrefix(amount, "-") {
		return ChainEvent{}, ErrNotDepositEvent
	}
	return ChainEvent{ProviderTransactionID: payload.Data.ID, AssetID: payload.Data.AssetID, DestinationAddress: payload.Data.DestinationAddress, DestinationTag: payload.Data.DestinationTag, Status: payload.Data.Status, TransactionHash: payload.Data.TxHash, BlockchainIndex: payload.Data.BlockchainIndex, BlockHash: payload.Data.BlockInfo.BlockHash, BlockHeight: payload.Data.BlockInfo.BlockHeight, Confirmations: payload.Data.Confirmations, Amount: amount}, nil
}

// AtomicAmount converts a decimal provider amount into exact asset atomic units
// without floating point rounding.
func AtomicAmount(amount string, decimals int16) (string, error) {
	amount = strings.TrimSpace(amount)
	if decimals < 0 || strings.HasPrefix(amount, "-") || amount == "" {
		return "", ErrNotDepositEvent
	}
	parts := strings.Split(amount, ".")
	if len(parts) > 2 || (len(parts) == 2 && (parts[0] == "" || parts[1] == "")) {
		return "", ErrNotDepositEvent
	}
	whole := parts[0]
	if whole == "" {
		whole = "0"
	}
	for _, char := range whole {
		if char < '0' || char > '9' {
			return "", ErrNotDepositEvent
		}
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if len(fraction) > int(decimals) {
		return "", ErrNotDepositEvent
	}
	for _, char := range fraction {
		if char < '0' || char > '9' {
			return "", ErrNotDepositEvent
		}
	}
	value := new(big.Int)
	if _, ok := value.SetString(whole+fraction+strings.Repeat("0", int(decimals)-len(fraction)), 10); !ok || value.Sign() <= 0 {
		return "", ErrNotDepositEvent
	}
	return value.String(), nil
}

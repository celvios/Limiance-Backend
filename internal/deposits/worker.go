package deposits

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/platform/queue"
)

type Receiver interface {
	Receive(context.Context, int32) ([]queue.ReceivedEvent, error)
	Delete(context.Context, string) error
}

type Worker struct {
	queue Receiver
	data  *datamanager.Manager
}

func NewWorker(receiver Receiver, data *datamanager.Manager) *Worker {
	return &Worker{queue: receiver, data: data}
}

func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	messages, err := w.queue.Receive(ctx, 10)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, message := range messages {
		if message.Event.Type != "custody.webhook_received" {
			return processed, errors.New("unexpected event on deposits queue")
		}
		var payload struct {
			ReceiptID string `json:"receipt_id"`
		}
		if err := json.Unmarshal(message.Event.Payload, &payload); err != nil || payload.ReceiptID == "" {
			return processed, errors.New("invalid custody webhook event")
		}
		raw, alreadyProcessed, err := w.data.WebhookPayload(ctx, payload.ReceiptID)
		if err != nil {
			return processed, err
		}
		if !alreadyProcessed && raw != nil {
			event, parseErr := ParseFireblocksEvent(raw)
			if parseErr != nil {
				return processed, fmt.Errorf("deposit webhook %s is not processable: %w", payload.ReceiptID, parseErr)
			}
			if isDepositStatus(event.Status) {
				decimals, found, lookupErr := w.data.DepositAssetDecimals(ctx, event.AssetID, event.DestinationAddress, event.DestinationTag)
				if lookupErr != nil {
					return processed, lookupErr
				}
				if !found {
					if err := w.data.MarkWebhookProcessed(ctx, payload.ReceiptID); err != nil {
						return processed, err
					}
					if err := w.queue.Delete(ctx, message.ReceiptHandle); err != nil {
						return processed, err
					}
					processed++
					continue
				}
				amountAtomic, amountErr := AtomicAmount(event.Amount, decimals)
				if amountErr != nil {
					return processed, amountErr
				}
				if _, err := w.data.ApplyDepositObservation(ctx, datamanager.DepositObservation{ProviderTransactionID: event.ProviderTransactionID, CustodyAssetID: event.AssetID, DestinationAddress: event.DestinationAddress, DestinationTag: event.DestinationTag, TransactionHash: event.TransactionHash, BlockchainIndex: event.BlockchainIndex, AmountAtomic: amountAtomic, Confirmations: event.Confirmations, BlockHash: event.BlockHash, BlockHeight: event.BlockHeight, Status: event.Status}); err != nil {
					return processed, err
				}
			}
			if event.ProviderTransactionID != "" {
				if _, err := w.data.RecordWithdrawalCustodyUpdate(ctx, datamanager.WithdrawalCustodyUpdate{ProviderTransactionID: event.ProviderTransactionID, Status: event.Status, TransactionHash: event.TransactionHash}); err != nil {
					return processed, err
				}
			}
			if err := w.data.MarkWebhookProcessed(ctx, payload.ReceiptID); err != nil {
				return processed, err
			}
		}
		if err := w.queue.Delete(ctx, message.ReceiptHandle); err != nil {
			return processed, err
		}
		processed++
	}
	return processed, nil
}

func isDepositStatus(status string) bool {
	return status == "CONFIRMING" || status == "COMPLETED" || status == "FAILED" || status == "REJECTED"
}

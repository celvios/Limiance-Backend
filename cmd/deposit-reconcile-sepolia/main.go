// deposit-reconcile-sepolia independently verifies a previously observed
// Sepolia ETH deposit and refreshes its confirmation count. It is deliberately
// narrow: production needs a durable, cursor-based chain watcher per network.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/platform/database"
)

const sepoliaChainID = "0xaa36a7"

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type receipt struct {
	BlockHash   string `json:"blockHash"`
	BlockNumber string `json:"blockNumber"`
	Status      string `json:"status"`
}

type transaction struct {
	To    string `json:"to"`
	Value string `json:"value"`
}

func main() {
	transactionHash := flag.String("transaction-hash", "", "Sepolia transaction hash to reconcile")
	rpcURL := flag.String("rpc-url", "", "Sepolia JSON-RPC HTTPS endpoint")
	apply := flag.Bool("apply", false, "persist the verified confirmation count")
	flag.Parse()
	if !strings.HasPrefix(*transactionHash, "0x") || len(*transactionHash) != 66 || !strings.HasPrefix(*rpcURL, "https://") {
		fatal("transaction-hash and HTTPS rpc-url are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, config.Load().DatabaseURL)
	if err != nil {
		fatal(err.Error())
	}
	defer pool.Close()
	data := datamanager.New(pool)
	deposit, err := data.DepositForReconciliation(ctx, *transactionHash)
	if err != nil {
		if err == pgx.ErrNoRows {
			deposit = datamanager.DepositReconciliation{
				ProviderTransactionID: *transactionHash,
				CustodyAssetID:        "ETH_TEST5",
				Network:               "ethereum_sepolia",
				TransactionHash:       *transactionHash,
			}
		} else {
			fatal(err.Error())
		}
	}
	if deposit.CustodyAssetID != "ETH_TEST5" || deposit.Network != "ethereum_sepolia" || deposit.DestinationTag != "" {
		fatal("deposit is not a direct ETH Sepolia deposit")
	}

	client := &http.Client{Timeout: 15 * time.Second}
	var chainID string
	call(ctx, client, *rpcURL, "eth_chainId", []any{}, &chainID)
	if strings.ToLower(chainID) != sepoliaChainID {
		fatal("RPC endpoint is not Ethereum Sepolia")
	}
	var receiptValue *receipt
	call(ctx, client, *rpcURL, "eth_getTransactionReceipt", []any{*transactionHash}, &receiptValue)
	if receiptValue == nil || receiptValue.BlockNumber == "" || receiptValue.Status != "0x1" {
		fatal("transaction is not a successful mined transaction")
	}
	var transactionValue transaction
	call(ctx, client, *rpcURL, "eth_getTransactionByHash", []any{*transactionHash}, &transactionValue)
	if deposit.DestinationAddress == "" {
		deposit.DestinationAddress = transactionValue.To
		deposit.AmountAtomic = hexToDecimal(transactionValue.Value)
		if _, found, err := data.DepositAssetDecimals(ctx, deposit.CustodyAssetID, deposit.DestinationAddress, ""); err != nil {
			fatal(err.Error())
		} else if !found {
			fatal("transaction destination is not an active ETH Sepolia deposit address")
		}
	}
	if !strings.EqualFold(transactionValue.To, deposit.DestinationAddress) {
		fatal("transaction destination does not match the deposit address")
	}
	if hexToDecimal(transactionValue.Value) != deposit.AmountAtomic {
		fatal("transaction amount does not match the observed deposit")
	}
	var head string
	call(ctx, client, *rpcURL, "eth_blockNumber", []any{}, &head)
	block := hexToBig(receiptValue.BlockNumber)
	latest := hexToBig(head)
	confirmations := new(big.Int).Sub(latest, block)
	confirmations.Add(confirmations, big.NewInt(1))
	if confirmations.Sign() < 1 || !confirmations.IsInt64() {
		fatal("invalid confirmation count")
	}

	fmt.Printf("verified tx=%s confirmations=%d apply=%t\n", *transactionHash, confirmations.Int64(), *apply)
	if !*apply {
		return
	}
	_, err = data.ApplyDepositObservation(ctx, datamanager.DepositObservation{
		ProviderTransactionID: deposit.ProviderTransactionID,
		CustodyAssetID:        deposit.CustodyAssetID,
		DestinationAddress:    deposit.DestinationAddress,
		TransactionHash:       deposit.TransactionHash,
		BlockHash:             receiptValue.BlockHash,
		BlockHeight:           block.String(),
		Confirmations:         int(confirmations.Int64()),
		AmountAtomic:          deposit.AmountAtomic,
		Status:                "CONFIRMING",
	})
	if err != nil {
		fatal(err.Error())
	}
	fmt.Println("deposit observation refreshed; eligible deposits are automatically credited")
}

func call(ctx context.Context, client *http.Client, url, method string, params []any, target any) {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		fatal(err.Error())
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		fatal(err.Error())
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		fatal(err.Error())
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		fatal("RPC request failed")
	}
	var decoded rpcResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		fatal(err.Error())
	}
	if decoded.Error != nil {
		fatal(decoded.Error.Message)
	}
	if err := json.Unmarshal(decoded.Result, target); err != nil {
		fatal(err.Error())
	}
}

func hexToBig(value string) *big.Int {
	result := new(big.Int)
	if _, ok := result.SetString(strings.TrimPrefix(value, "0x"), 16); !ok {
		fatal("invalid RPC hexadecimal quantity")
	}
	return result
}

func hexToDecimal(value string) string { return hexToBig(value).String() }

func fatal(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}

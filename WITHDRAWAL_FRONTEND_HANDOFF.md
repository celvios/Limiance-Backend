# Withdrawal Frontend Handoff

The withdrawal backend now supports the full custody handoff through signed Fireblocks webhooks. Customer withdrawals remain protected by KYC, withdrawal-address cooldown, TOTP step-up, idempotency, and the existing two-person treasury approval gate.

## Customer API flow

1. Load enabled assets from `GET /v1/assets/catalog`.
2. Load the customer address book from `GET /v1/withdrawal-addresses`.
3. Before adding an address or submitting a withdrawal, obtain a five-minute step-up token:

```http
POST /v1/security/step-up
```

```json
{ "purpose": "withdrawal_address_added", "code": "123456" }
```

Use `purpose: "withdrawal_created"` for the withdrawal request. Send the returned token in `X-Step-Up-Token`.

4. Add a destination address. The label is required:

```http
POST /v1/withdrawal-addresses
X-Step-Up-Token: <token>
```

```json
{
  "asset_symbol": "ETH",
  "network": "ethereum_sepolia",
  "address": "0x...",
  "tag": "",
  "label": "My test wallet"
}
```

New addresses may be returned as `pending` during the configured cooldown. Do not allow withdrawal submission until the address is `active`.

5. Submit the withdrawal. The `amount_atomic` value is an integer atomic amount and should be sent as a JSON string to preserve precision:

```http
POST /v1/wallet/withdrawals
Idempotency-Key: <unique key, 16-255 characters>
X-Step-Up-Token: <token>
```

```json
{
  "source_account_id": "<funding-account-id>",
  "asset_symbol": "ETH",
  "network": "ethereum_sepolia",
  "address": "0x...",
  "tag": "",
  "amount_atomic": "1000000000000000"
}
```

The backend currently accepts the JSON number form for compatibility, but the frontend must use a string. A first submission returns `201`; a retry with the same idempotency key returns the existing result.

## Status display

`GET /v1/wallet/withdrawals?limit=25` returns the authoritative status:

| Status | Customer message | Actions |
| --- | --- | --- |
| `pending_approval` | Withdrawal request is awaiting approval. | Cancel |
| `approved` | Withdrawal approved and waiting for custody submission. | No cancel |
| `submitted` | Withdrawal submitted to the blockchain provider. | No cancel |
| `completed` | Withdrawal completed. | Show transaction hash when present |
| `failed` | Withdrawal failed and funds were returned to your available balance. | Contact support / retry |
| `cancelled` | Withdrawal cancelled and funds were returned. | None |
| `rejected` | Withdrawal rejected. | Show support guidance |

Never label `approved` or `submitted` as completed. The transaction hash is only authoritative when returned by the API.

## Travel Rule

When required by policy, capture the following fields and submit them while the withdrawal is still `pending_approval`:

```http
POST /v1/withdrawals/{withdrawal_id}/travel-rule
```

```json
{
  "originator_name": "...",
  "originator_address": "...",
  "beneficiary_name": "...",
  "beneficiary_address": "...",
  "beneficiary_country": "NG",
  "transfer_purpose": "personal transfer"
}
```

The backend encrypts this data before persistence. Do not render or log the stored ciphertext.

## Cancellation and refresh

Cancel only while the status is `pending_approval`:

```http
POST /v1/withdrawals/{withdrawal_id}/cancel
```

After submission, refresh withdrawal history and balances. Custody status changes arrive asynchronously, so polling the history endpoint every 10-15 seconds while a withdrawal is non-terminal is acceptable until a push channel is available.

## Admin-only operations

These do not belong in the customer frontend:

- `POST /v1/admin/withdrawals/{withdrawal_id}/approvals`
- `GET /v1/admin/operational-controls/withdrawals`
- `PUT /v1/admin/operational-controls/withdrawals`

The approval endpoint requires a treasury approver and two distinct approvers are required overall. After the second approval, the backend worker submits the withdrawal to Fireblocks. Verified Fireblocks webhooks advance the request and settle the held balance exactly once.
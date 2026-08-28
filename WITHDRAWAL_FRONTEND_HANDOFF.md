# Withdrawal Frontend Handoff

The withdrawal backend now supports the full custody handoff through signed Fireblocks webhooks. Customer withdrawals remain protected by KYC, withdrawal-address cooldown, TOTP step-up, idempotency, and the existing two-person treasury approval gate.

## Frontend implementation status

The customer frontend already calls the core withdrawal, address-book, cancellation, and step-up endpoints. The following items still need frontend work before the withdrawal acceptance flow is complete:

- Preserve the exact atomic amount. The current backend JSON contract accepts `amount_atomic` as an integer number, so the current UI must reject values beyond JavaScript's safe integer range until the API is changed to decode decimal strings.
- Filter the network selector by the selected asset and only show enabled catalog routes.
- Make the address label required because the backend rejects an empty label.
- Add status rendering for `submitted`, `completed`, and `failed`, including the transaction hash only when the API returns one.
- Add Travel Rule capture when compliance policy requires it.
- Refresh balances and withdrawal history after submission, and poll non-terminal withdrawals every 10-15 seconds until a push channel is available.
- Keep cancellation enabled only for `pending_approval`.

The customer frontend must not implement approval, custody submission, blockchain reconciliation, or the withdrawal kill switch.

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

5. Submit the withdrawal. The current backend expects `amount_atomic` as an integer JSON number:

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
  "amount_atomic": 1000000000000000
}
```

Do not convert large atomic values through JavaScript `Number()` without a safe-integer check. A backend follow-up should add decimal-string decoding before the frontend sends values larger than `Number.MAX_SAFE_INTEGER`. A first submission returns `201`; a retry with the same idempotency key returns the existing result.

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

## Error mapping

| HTTP/status error | Frontend behavior |
| --- | --- |
| `401 unauthenticated` | Refresh auth state and route to login. |
| `400 invalid_withdrawal` | Show an inline validation message; do not retry automatically. |
| `401 step_up_required` | Reopen the TOTP step-up modal and obtain a fresh token. |
| `409 withdrawal_rejected` | Show that the request was rejected by policy or validation. |
| `409 withdrawal_not_cancellable` | Refresh history because the request moved beyond the cancellable state. |
| `503 withdrawals_temporarily_disabled` | Show that withdrawals are temporarily disabled. |
| `503 withdrawal_rejected` | Show a temporary processing error and allow a new idempotency key for retry only after confirming the original request did not succeed. |

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

## Observability handoff

Observability is primarily an operations concern and does not require customer-facing UI. The API exposes:

```http
GET /metrics
```

The frontend team does not need to call this endpoint from browser code. It should be scraped server-to-server by Prometheus. Do not expose Prometheus or Grafana credentials in the customer application.

Available API metrics include:

- `limiance_http_requests_total{method,path,status}`
- `limiance_http_request_duration_seconds{method,path}`

The `path` label normalizes UUIDs to `:id`; do not create dashboards or alerts using raw customer IDs.

For customer UI behavior, use the withdrawal API as the source of truth. The frontend should refresh balances/history after a mutation, while operators use the Prometheus/Grafana stack to monitor request rate, error rate, latency, API availability, and the withdrawal worker service.

Local operator configuration is available through:

- `observability/prometheus.yml`
- `observability/alerts.yml`
- `observability/limiance-dashboard.json`

Start the local monitoring services with:

```powershell
docker compose --profile observability up
```

The customer frontend does not need a new environment variable for observability. Its existing `VITE_API_BASE_URL` must point to the deployed API, for example `https://api.staging.celvios.site`.
# Limiance Trading Frontend Handoff

Last verified: 2026-09-02  
Environment: staging  
API base URL: `https://api.staging.celvios.site`  
WebSocket URL: `wss://api.staging.celvios.site/v2/ws`  
Contract source: `openapi/exchange-v2.yaml`

## Current activation state

The v2 market, order, and P2P contracts are deployed. The matching engine, settlement consumer, market-data worker, API, and withdrawal worker are running.

Spot execution is not yet open for customer money:

- every market currently reports `status: "halted"`;
- the internal market maker evaluates 18 approved pairs in reference-only dry-run after distinct maker-checker approval;
- its dedicated UTA has no inventory and reference-only commands never reach the matching engine;
- ticker trade fields therefore correctly return zero prices and zero volume until real trades exist;
- `reference_price`, `reference_observed_at`, and `reference_status` expose the independent-venue median without fabricating a trade.

Frontend development can proceed against the deployed contracts. Gate order entry when a market is halted and never replace zero exchange data with invented trades.

## Shared client rules

Use the session cookie for the browser application:

```ts
const API_BASE = 'https://api.staging.celvios.site';

export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(`${API_BASE}${path}`, {
    ...init,
    credentials: 'include',
    headers: {
      Accept: 'application/json',
      ...(init.body ? { 'Content-Type': 'application/json' } : {}),
      ...init.headers,
    },
  });

  const body = await response.json();
  if (!response.ok) throw body;
  return body as T;
}
```

Rules:

- Keep every price, quantity, balance, fee, and volume as a decimal string or `bigint`. Never pass money through JavaScript `Number`.
- Generate a new UUID idempotency key for each user-intended mutation. Reuse that same key only when retrying the identical request.
- Treat `401` as an expired or missing session and `429` as a server-directed retry.
- Read `X-RateLimit-Limit`, `X-RateLimit-Remaining`, and `X-RateLimit-Reset` where present.
- Display `error.message`, branch on `error.code`, and include `error.request_id` in support reports.

The v2 error envelope is:

```json
{
  "error": {
    "code": "INSUFFICIENT_BALANCE",
    "message": "available balance is insufficient",
    "request_id": "0123456789abcdef"
  }
}
```

## Atomic-unit formatting

Market metadata provides the scales needed for display:

```ts
export function formatAtomic(value: string, scale: number): string {
  const negative = value.startsWith('-');
  const digits = negative ? value.slice(1) : value;
  const padded = digits.padStart(scale + 1, '0');
  const whole = padded.slice(0, -scale || undefined);
  const fraction = scale === 0 ? '' : padded.slice(-scale).replace(/0+$/, '');
  return `${negative ? '-' : ''}${whole}${fraction ? `.${fraction}` : ''}`;
}
```

Use `price_scale` for prices and quote values, and `quantity_scale` for base quantities. `change_bps_24h` is a signed integer string: divide by 100 for a display percentage without converting the underlying monetary fields.

## Markets and leaderboards

Public REST routes require no authentication:

| Purpose | Route |
| --- | --- |
| Pair catalog and scales | `GET /v2/market/markets` |
| All 24-hour tickers | `GET /v2/market/tickers` |
| One ticker | `GET /v2/market/ticker/{pair}` |
| L2 order book | `GET /v2/market/orderbook/{pair}?depth=20` |
| Recent trades | `GET /v2/market/trades/{pair}?limit=100` |
| Candles | `GET /v2/market/klines/{pair}?interval=1m&limit=500` |

Supported candle intervals are `1m`, `5m`, `15m`, `1h`, `4h`, `1d`, `1w`, and `1M`.

Build market tables as follows:

- Top gainers: exclude zero-volume markets, then sort `BigInt(change_bps_24h)` descending.
- Top losers: exclude zero-volume markets, then sort `BigInt(change_bps_24h)` ascending.
- Top volume: sort `BigInt(quote_volume_24h)` descending.
- New or inactive markets: retain them in the catalog but show `--` instead of a fabricated percentage when `sequence_id === 0`.
- A zero-trade market may display `reference_price` as its current indicative price when `reference_status === "fresh"`. Poll REST tickers for reference updates; reference cycles do not publish trade WebSocket events. Never copy it into `last_price`, volume, candles, or 24-hour change.
- The API suppresses reference values when stopped, disabled, expired, or awaiting a decision after configuration. `reference_observed_at` is the accepted decision timestamp, not an exchange-provided timestamp.
- Disable the trade form whenever catalog status is `halted`.

The order-book level tuple is `[price_atomic, quantity_atomic, order_count]`.

## WebSocket market stream

Connect to `wss://api.staging.celvios.site/v2/ws`. The compatibility URL `/ws/v2/market` is also deployed, but new clients should use `/v2/ws`.

Subscribe after connection:

```json
{"action":"subscribe","channel":"orderbook.BTCUSDT","depth":20}
```

Channels:

- `orderbook.PAIR`
- `trades.PAIR`
- `ticker.PAIR`
- `klines.PAIR.INTERVAL`

Unsubscribe with the same channel and `action: "unsubscribe"`.

The server sends a snapshot before incremental events. Track `sequence_id` per channel. If an incremental sequence is not the previous value plus one, discard local state, reconnect, and wait for a fresh snapshot. Respond to normal WebSocket ping frames; the server heartbeat interval is approximately 25 seconds. Use exponential reconnect backoff with jitter and cap it at 30 seconds.

Do not apply an order-book delta before its snapshot. For a price level, quantity `"0"` removes the level.

## Spot orders

Browser sessions and appropriately scoped HMAC API keys are supported.

| Action | Route |
| --- | --- |
| Place | `POST /v2/orders` |
| Open orders | `GET /v2/orders?pair=BTCUSDT&status=OPEN&limit=100&offset=0` |
| History | `GET /v2/orders/history?pair=BTCUSDT&limit=100&offset=0` |
| Details | `GET /v2/orders/{order_id}` |
| Cancel | `DELETE /v2/orders/{order_id}` |

Place and cancel require `Idempotency-Key`.

```json
{
  "pair": "BTCUSDT",
  "side": "BUY",
  "type": "LIMIT",
  "price": "5000000000000",
  "quantity": "1000000",
  "time_in_force": "GTC",
  "post_only": false,
  "reduce_only": false
}
```

Order types: `LIMIT`, `MARKET`, `STOP_LIMIT`, `STOP_MARKET`, and `TAKE_PROFIT_LIMIT`. Time in force: `GTC`, `IOC`, and `FOK`.

Only submit `price` when required by the order type. Conditional orders require `trigger_price`. Preserve server statuses exactly: `PENDING`, `TRIGGER_PENDING`, `OPEN`, `PARTIALLY_FILLED`, `FILLED`, `CANCELED`, and `REJECTED`.

Do not optimistically release held balances on cancellation. Refresh the order and wallet balances after the server confirms the terminal state.

## P2P escrow

P2P uses the Funding Account and an off-platform fiat confirmation. Limiance does not process the fiat leg.

Customer routes:

- `POST /v2/p2p/trades`
- `GET /v2/p2p/offers`
- `GET /v2/p2p/trades`
- `GET /v2/p2p/trades/{trade_id}`
- `POST /v2/p2p/trades/{trade_id}/accept`
- `POST /v2/p2p/trades/{trade_id}/mark-paid`
- `POST /v2/p2p/trades/{trade_id}/release`
- `POST /v2/p2p/trades/{trade_id}/cancel`
- `POST /v2/p2p/trades/{trade_id}/disputes`
- `POST /v2/p2p/trades/{trade_id}/evidence`

Every mutation requires `Idempotency-Key`. Status progression is:

```text
open -> accepted -> paid -> released
                    \-> disputed -> released | refunded
open -> cancelled | expired
accepted -> expired/refunded when the payment deadline passes
```

`mark-paid` records only the buyer's assertion that payment occurred. It must not display a Limiance fiat-payment receipt. Evidence submission stores an immutable object reference and SHA-256 digest; upload handling must be completed before calling the evidence endpoint.

Operations routes:

- `GET /v2/admin/p2p/disputes`
- `POST /v2/admin/p2p/trades/{trade_id}/resolutions`
- `POST /v2/admin/p2p/resolutions/{resolution_id}/approve`

Force release and refund require a treasury operator proposal followed by a different treasury approver.

## HMAC API clients

Non-browser integrations send:

- `X-API-Key`
- `X-API-Timestamp` as Unix seconds, within 30 seconds
- `X-API-Nonce`, unique and 16-128 characters
- `X-API-Signature`, lowercase hexadecimal HMAC-SHA256

Canonical input:

```text
timestamp\nnonce\nMETHOD\nescaped_path\nraw_query\nhex(sha256(raw_body))
```

The frontend must not embed API secrets. HMAC signing belongs in server-side customer integrations only.

## Frontend rollout order

1. Replace market mocks with `/v2/market/markets` and `/v2/market/tickers`.
2. Add atomic formatting and halted-market UI gating.
3. Add WebSocket snapshots, sequence tracking, reconnect, and REST fallback.
4. Wire order placement, open orders, history, details, cancellation, and wallet refresh.
5. Wire P2P offer discovery, participant state actions, countdowns, evidence, and disputes.
6. Wire the admin P2P maker-checker queue in the admin application.
7. Run browser tests against halted markets first; enable live-order tests only after the backend activation gate is signed off.

## Do not expose as complete

- Derivatives, futures, and options are outside the current backend scope.
- Proprietary fiat/card payment processing is deferred until a provider is selected.
- Rewards, referrals, data export, P2P chat, and advanced chart drawing tools are not implied by the v2 exchange contract.
- A page existing in the frontend is not proof that its backend workflow exists.

## Acceptance checklist

- [ ] No money value passes through JavaScript `Number`.
- [ ] Halted markets disable order submission.
- [ ] Zero-sequence tickers render an empty/inactive state, not fake movement.
- [ ] Leaderboards use integer basis points and quote volume.
- [ ] WebSocket gaps force a snapshot reload.
- [ ] All order and P2P mutations use stable idempotency keys.
- [ ] `401`, `409`, `422`, `429`, and `503` have explicit UI states.
- [ ] Order cancellation refreshes balances from the backend.
- [ ] P2P action buttons follow the server state and participant role.
- [ ] Force-release/refund requires two distinct authorized administrators.

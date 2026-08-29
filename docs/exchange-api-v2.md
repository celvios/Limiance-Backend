# Limiance Exchange API v2

The canonical machine-readable contract is `openapi/exchange-v2.yaml`. All monetary values are integer atomic-unit strings. Clients must apply the market's published price and quantity scales for display; they must not send JSON floating-point money values.

## Authentication and signing

Browser clients can use the secure `limiance_session` cookie. API clients send `X-API-Key`, `X-API-Timestamp`, `X-API-Nonce`, and `X-API-Signature`.

`X-API-Timestamp` is Unix seconds and must be within 30 seconds of server time. `X-API-Nonce` is a unique 16–128 character URL-safe value and cannot be reused. The signature is lowercase hexadecimal HMAC-SHA256 using the API secret over:

```text
timestamp + "\n" +
nonce + "\n" +
UPPERCASE_HTTP_METHOD + "\n" +
escaped_path + "\n" +
raw_query_without_question_mark + "\n" +
lowercase_hex_sha256_of_raw_body
```

The raw query is signed, so clients must not reorder or re-encode it after signing. A tampered body, path, query, nonce, or timestamp is rejected. API-key scopes are `read_only`, `trade`, and `withdraw`; order placement and cancellation require `trade`. Optional IP entries accept exact IPv4/IPv6 addresses or CIDR networks.

## Idempotency

Send `Idempotency-Key` for every order placement and cancellation. Retrying an identical operation returns the original result. Reusing the key with a different request returns `IDEMPOTENCY_CONFLICT`. Keep keys stable across network retries and unique across distinct business operations.

## REST and errors

Public market endpoints live under `/v2/market`. Authenticated order endpoints live under `/v2/orders`. Every v2 error uses:

```json
{"error":{"code":"INVALID_ORDER","message":"order parameters are invalid","request_id":"0123456789abcdef"}}
```

Include `request_id` in support reports. Rate-limit responses expose `X-RateLimit-Limit`, `X-RateLimit-Remaining`, and `X-RateLimit-Reset`; HTTP 429 also returns `Retry-After`. Trading endpoints fail closed if the shared Redis limiter is unavailable.

## WebSocket

Connect to `/v2/ws` (the compatibility endpoint `/ws/v2/market` is also served). Subscribe with:

```json
{"action":"subscribe","channel":"orderbook.BTCUSDT","depth":20}
```

Channels are `orderbook.PAIR`, `trades.PAIR`, `ticker.PAIR`, and `klines.PAIR.INTERVAL`. Reconnecting clients receive a current snapshot before incrementals. Every incremental includes a monotonic `sequence_id`; reconnect if continuity is lost. Send `{"action":"unsubscribe","channel":"..."}` to stop a channel. The server uses ping/pong heartbeats and disconnects clients whose bounded outbound queue remains full.

## Local mock server

Run `go run ./cmd/api-mock -address :4010`. It serves deterministic REST examples and a WebSocket subscription flow without PostgreSQL, Redis, or the matching engine. It is for frontend contract development only and does not implement authentication semantics.

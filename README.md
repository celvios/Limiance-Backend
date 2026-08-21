# Limiance Backend

Phase 1 backend for Limiance Exchange, built with Go 1.26 and the Go standard library HTTP stack.

## Current foundation

- `net/http` API server with strict server timeouts, request IDs, security headers, panic recovery, and health endpoints.
- Password login uses an opaque, server-stored session: PostgreSQL keeps only a SHA-256 token hash; the raw credential is sent only as an HTTP-only, SameSite=Strict cookie.
- PostgreSQL-first immutable double-entry ledger design, transactional outbox schema, audit-event schema, and asset registry. `GET /v1/assets/catalog` exposes the approved 25-asset/13-network policy catalog.
- A transaction-scoped **Data Manager** owns persistence: domain services use repositories for account, ledger, audit, and outbox changes; HTTP handlers and provider adapters never write PostgreSQL directly.
- Docker development services for PostgreSQL and Redis.
- OpenAPI contract scaffold and a tested journal-balance invariant.

## Custody boundary

Limiance owns the ledger, Deposit Gateway, asset policy, risk workflow, and
wallet orchestration. Fireblocks remains an optional custody adapter. The
production-scale Limiance custody architecture uses a provider-neutral wallet
registry and an isolated signing boundary; the API process has no raw signing
key or KMS signing permission. The first Limiance-owned path is testnet-only.
It cannot custody mainnet funds until an audited MPC/HD wallet engine,
key-management ceremony, recovery drills, operational runbooks, and release
approval are complete.

## Providers

- Sumsub: KYC/KYB and screening
- Fireblocks: MPC custody/signing
- Yellow Card: primary African fiat rail
- cNGN: Nigerian NGN funding/settlement rail
- Twilio: SMS fallback
- SendGrid: transactional email
- FCM/APNs: mobile push

### Integration rules

Each provider is researched against its official documentation before implementation. Sumsub requests use timestamped HMAC-SHA256 signing; inbound Sumsub webhooks are verified against the unmodified payload using `X-Payload-Digest` and its declared digest algorithm. Fireblocks uses the current V2 webhook programme and Limiance's ledger as the financial source of truth. No provider webhook credits a user directly.

The Fireblocks adapter uses the official REST API's per-request RS256 JWT: `uri`, unique nonce, issue/expiry time, API-user subject, and SHA-256 body hash are signed by the RSA private key held only in Limiance's server environment. It creates an MPC vault account using an anonymised Limiance reference, then creates an asset wallet at `POST /v1/vault/accounts/{vaultAccountId}/{assetId}`. This route is intentionally used for account-based networks; Fireblocks reserves the separate `/addresses` API for UTXO and tag/memo assets. Configure `FIREBLOCKS_API_KEY`, `FIREBLOCKS_PRIVATE_KEY`, and `FIREBLOCKS_BASE_URL` only after a sandbox API user and least-privilege policy are ready.

`POST /v1/webhooks/fireblocks` implements Fireblocks Webhooks V2 verification with the EU JWKS endpoint and the `Fireblocks-Webhook-Signature` detached RS512 JWS header. Only the original raw body is verified. A valid event is deduplicated by its SHA-256 digest, persisted, and written to the outbox for the Deposit Gateway worker; an invalid event is rejected and neither case can directly credit a customer.

The `cmd/deposits` worker consumes only `custody.webhook_received` events from `limiance-deposits`. It accepts only Fireblocks `transaction.created` and `transaction.status.updated` transfer events, matches the exact enabled Fireblocks asset ID and destination address/tag, and converts decimal provider amounts to exact atomic units. Deposits move to `confirming` until Limiance's configured confirmation threshold is met, then to `pending_risk_review`. Terminal failed/rejected reports become `reorged`. The worker does not contain a credit operation: only a future, separately authorised risk-review/ledger-credit command may transition a deposit to `credited`.

`POST /v1/admin/deposits/{deposit_id}/approve` is the implemented credit command. It requires an authenticated `compliance` role, a meaningful review reason, a `pending_risk_review` deposit, and an approved customer KYC profile. In one transaction it writes the custody-clearing debit and customer Funding Account credit, marks the deposit/risk status approved, creates an audit event, and writes `deposit.credited` to the outbox. It is not a customer-facing endpoint and cannot be invoked by the deposit worker or webhook handler.

The SendGrid verification adapter uses the documented `POST /v3/mail/send` endpoint, Bearer API keys, and a dynamic template. It is intentionally unavailable until `SENDGRID_API_KEY`, `SENDGRID_FROM_EMAIL`, and `SENDGRID_VERIFICATION_TEMPLATE_ID` are provided. Twilio Verify v2—not the legacy v1 API—handles SMS verification start/check operations using `TWILIO_API_KEY`, `TWILIO_API_SECRET`, and `TWILIO_VERIFY_SERVICE_SID`; only E.164 phone numbers are accepted.

Provider secrets belong in AWS Secrets Manager in deployed environments. Do not put credentials in source control or logs.

## Persistence and asynchronous work

`DATABASE_URL` is infrastructure configuration only: it creates the PostgreSQL connection pool passed to the Data Manager. All application/business reads and writes go through the Data Manager. The connection factory and migration command are the deliberate infrastructure exceptions; migrations manage schema, not exchange business workflows.

Phase 1 uses PostgreSQL's transactional outbox plus **AWS SQS** for durable asynchronous work. RabbitMQ is not part of this architecture. The outbox publisher writes to the shared `limiance-outbox` queue; the event router then copies each event to its dedicated worker queue before acknowledging the source message. `email.verification_requested` is routed to `limiance-notifications`; events with no configured route are preserved in `limiance-unrouted` for operations rather than discarded. Dedicated workers never consume the shared outbox queue. Redis is reserved for rate limits, distributed locks, and short-lived cache/session support—not durable financial workflows.

The SQS adapter uses the official AWS SDK for Go v2. Configure `AWS_REGION`, `SQS_OUTBOX_QUEUE_URL`, `SQS_NOTIFICATIONS_QUEUE_URL`, and `SQS_UNROUTED_QUEUE_URL`; production resolves AWS credentials through the standard AWS credential chain/role, while `SQS_ENDPOINT` is reserved for a local emulator.

## Local development

1. Copy `.env.example` to `.env` and populate only development/sandbox credentials. The development process loads this Git-ignored file; explicitly supplied environment variables take precedence.
2. Start local dependencies with `docker compose up -d`.
3. Apply migrations with `$env:DATABASE_URL='postgres://limiance:development-only@localhost:15432/limiance?sslmode=disable'; go run ./cmd/migrate`.
4. Run `go test ./...`.
5. Run the API with `go run ./cmd/api`.
6. Verify `GET http://localhost:8080/healthz` and `GET http://localhost:8080/readyz`.

## Financial invariants

- Monetary amounts are atomic integer units; floating point is prohibited.
- Every posted journal is balanced per asset.
- State-changing financial endpoints require an idempotency key.
- No provider webhook may directly mutate a user balance; it must pass through validated workflow and ledger posting.
- High-risk withdrawals require two distinct operational approvers.
- A platform-administrator withdrawal kill switch is enforced in the same
  database transaction as withdrawal creation and approval. It blocks all new
  requests and approvals, writes audit/outbox records when changed, and must be
  enabled during any custody, chain, or security incident.
- Internal transfers settle only through an immutable journal: a single transaction writes the transfer record, balanced debit/credit postings, audit event, and outbox event. Source debit commands are serialised per account/asset pair and require an idempotency key.
- A catalog entry is not a live deposit route. Each asset/network pair remains disabled until its custody configuration, official token metadata, confirmation policy, and testnet/sandbox checks are approved.

## Implemented account and ledger API

- `POST /v1/transfers` posts a user-authorised internal transfer. It accepts a Funding or UTA source/destination account, enabled asset symbol/network pair, positive atomic amount, and an `Idempotency-Key` header (16–255 characters). A first request returns `201`; a replay returns `200` with the original transfer and journal IDs.
- `GET /v1/accounts/balances` returns the authenticated user's balances calculated from posted journal entries. Each row exposes `available_atomic`, `held_atomic`, `pending_atomic`, and `locked_atomic` as decimal strings, never floats.
- `POST /v1/deposits/addresses` returns the authenticated user's active deposit address for an enabled `asset_symbol`/`network` pair. The route is unavailable until the registry contains both `status = enabled` and verified custody configuration; catalog approval is deliberately insufficient. Address issuance records a provider-separated wallet/address mapping, audit event, and outbox event but **never** credits a balance.
- Neither endpoint trusts a client-side balance or calls PostgreSQL from HTTP code. The Data Manager owns the transaction and the ledger is the financial source of truth.

## Delivery sequence

The current foundation includes registration, email verification, password login, server-stored sessions with active-session management, enforced TOTP login, logout, emergency account freeze with session revocation and audit trail, Twilio Verify integration, SendGrid delivery integration, KYC state foundations, authenticated Sumsub SDK-session issuance, verified Sumsub webhook signatures, the asset/network catalog, transactional-outbox routing, and ledger-backed internal transfers/balances. Custody can run through Fireblocks or the isolated self-custody **testnet** adapter; the latter does not make mainnet custody ready. Withdrawals, address-book cooldowns, cancellation, Travel Rule encryption, notification persistence, and an administrator withdrawal kill switch are also implemented. Conversion, fiat rails, production signer/wallet infrastructure, actual FCM/APNs delivery, passkeys, and the isolated admin application remain work in progress.

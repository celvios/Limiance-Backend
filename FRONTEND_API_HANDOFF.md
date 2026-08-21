# Limiance Frontend API Integration Handoff

This document is the source of truth for the currently implemented backend contract.

## Non-negotiable rules

- Do not invent endpoints, request fields, response fields, balances, prices, transaction states, or provider flows.
- If a screen needs data or an action not listed here, stop and ask the client for a backend endpoint. Record the missing endpoint and its purpose.
- Do not use mocked financial values once an implemented endpoint exists.
- The backend uses an HTTP-only session cookie. Never store a session token in `localStorage`, `sessionStorage`, React state, or a URL.
- Every `fetch` call must use `credentials: 'include'`.
- For development, use `VITE_API_BASE_URL=http://127.0.0.1:18080`. Production will use the deployed API origin.
- Money is always an atomic integer. The frontend must never submit floating-point numbers. Convert user display input with the asset's known decimals only when those decimals are made available by the backend.
- State-changing money endpoints require an `Idempotency-Key` request header with a new UUID/random value per user action; reuse it only to retry the exact same action.

## Shared API client

Create one client module, for example `src/api/client.js`:

```js
const baseURL = import.meta.env.VITE_API_BASE_URL || 'http://127.0.0.1:18080';

export async function api(path, options = {}) {
  const response = await fetch(`${baseURL}${path}`, {
    credentials: 'include',
    headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
    ...options,
  });
  const body = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw Object.assign(new Error(body.error || 'Request failed'), { status: response.status });
  }
  return body;
}
```

Show safe user-facing messages. Never expose raw provider/API errors, credentials, access tokens, or internal IDs in console logs.

## Authentication

### Register

`POST /v1/auth/register`

```json
{
  "email": "person@example.com",
  "password": "AtLeast12Chars1",
  "country_code": "NG"
}
```

Success `202`:

```json
{
  "user_id": "uuid",
  "uid": 123456,
  "status": "pending_verification",
  "funding_account_id": "uuid",
  "uta_account_id": "uuid"
}
```

Backend password rule: 12–128 characters, uppercase, lowercase, and a digit. Update the registration UI from its current 8–30-character display to this rule.

### Verify email

`POST /v1/auth/email/verify`

```json
{ "email": "person@example.com", "code": "123456" }
```

Success `200`:

```json
{ "status": "email_verified" }
```

On `400 invalid_or_expired_code`, keep the user on the code screen and allow correction.

### Resend email verification

`POST /v1/auth/email/resend`

```json
{ "email": "person@example.com" }
```

Success is always `202` with `{ "status": "if_required_sent" }`, including for unknown or already-verified emails. Show a neutral “If the account needs verification, a new code has been sent” message. The previous unconsumed code becomes invalid when a new one is issued.

### Login

`POST /v1/auth/login`

```json
{ "email": "person@example.com", "password": "AtLeast12Chars1" }
```

Success `200`:

```json
{ "status": "authenticated" }
```

The browser receives the HttpOnly `limiance_session` cookie automatically. Then load `GET /v1/auth/session`.

### Current session and logout

- `GET /v1/auth/session` returns `{ "user_id": "uuid", "uid": 123456, "email": "person@example.com" }`.
- `POST /v1/auth/logout` returns `{ "status": "logged_out" }`.

On `401`, clear frontend user state and go to login. Do not attempt to clear the HttpOnly cookie in JavaScript.

### Freeze account

`POST /v1/auth/freeze` requires the authenticated session cookie and has no
request body. It is an emergency, one-way containment action: it freezes the
account and revokes every active session in one atomic backend transaction.

Success `200`:

```json
{ "status": "account_frozen" }
```

Clear customer state and route to the support/recovery screen. There is no
customer unfreeze route; do not add one to the UI. A later login or authenticated
request for this user returns `403 account_frozen`.

### TOTP authenticator app

TOTP is enforced during login only after the customer has completed enrollment.
Never store the returned secret, `otpauth_uri`, or `mfa_token` in browser
storage or logs.

1. Authenticated `POST /v1/security/totp/enroll` has an empty body and returns
   `201`:

```json
{ "secret": "BASE32-SECRET", "otpauth_uri": "otpauth://totp/..." }
```

Display the QR generated from `otpauth_uri` immediately, then discard both
fields. The customer enters their six-digit code to confirm:

```text
POST /v1/security/totp/confirm
{ "code": "123456" }
```

Success is `{ "status": "totp_enabled" }`. Do not show TOTP as enabled until
this confirmation succeeds.

2. A normal `POST /v1/auth/login` now returns `202` when TOTP is enabled:

```json
{ "status": "mfa_required", "mfa_token": "short-lived-opaque-token" }
```

Keep `mfa_token` only in memory, show the authenticator-code screen, and call:

```text
POST /v1/auth/totp/verify
{ "mfa_token": "short-lived-opaque-token", "code": "123456" }
```

Successful verification returns `200 { "status": "authenticated" }` and sets
the ordinary HttpOnly session cookie. An invalid code consumes the one-time MFA
challenge; restart password login rather than offering unlimited retries.

3. To turn TOTP off, the authenticated customer must provide a current code:

```text
DELETE /v1/security/totp
{ "code": "123456" }
```

Success is `{ "status": "totp_disabled" }`.

### Active sessions (trusted-device management)

`GET /v1/security/sessions` lists the customer's currently active sessions:

```json
{
  "sessions": [{
    "id": "uuid",
    "user_agent": "browser identification",
    "client_ip": "ip-address",
    "created_at": "2026-08-21T12:00:00Z",
    "last_seen_at": "2026-08-21T12:10:00Z",
    "current": true
  }]
}
```

Treat this as an active-session/device management screen, not a claim that a
browser is cryptographically trusted. Do not derive a device fingerprint in the
frontend.

`DELETE /v1/security/sessions/{session_id}` revokes a selected active session.
If the response has `current: true`, clear customer state and route to login;
the backend has also cleared the session cookie.

### Anti-phishing code

`PUT /v1/security/anti-phishing-code` requires a logged-in customer:

```json
{ "code": "MY-LIMIANCE-CODE" }
```

The code must be 4–32 uppercase letters, digits, hyphens, or underscores. An
empty code clears it. The value is never returned by the API and must never be
logged. It is saved now for the security-email renderer; do not claim every
existing email template displays it until the security-email delivery workflow
is enabled.

### Phone verification

Both routes require the authenticated session cookie. Phone numbers must be E.164, for example `+2348012345678`.

`POST /v1/auth/phone/start`

```json
{ "phone": "+2348012345678" }
```

Success `202`: `{ "status": "pending" }`. This asks Twilio Verify to send an SMS code.

`POST /v1/auth/phone/verify`

```json
{ "phone": "+2348012345678", "code": "123456" }
```

Success `200`: `{ "status": "phone_verified" }`. Handle `400 invalid_or_expired_code`, `409 phone_in_use`, and `503 phone_verification_unavailable`.

## KYC

`POST /v1/kyc/sessions` has an empty JSON body `{}`.

Success `201`:

```json
{
  "provider": "sumsub",
  "access_token": "short-lived-token",
  "expires_in_seconds": 600
}
```

The token is only for launching Sumsub WebSDK immediately. Do not persist or log it. The frontend still needs the official Sumsub WebSDK package/integration work; ask for approval before adding that dependency. Handle:

- `409 kyc_already_approved`: show verified state.
- `503 kyc_unavailable`: show temporary unavailable state.

### KYC status

`GET /v1/kyc/status` returns the durable, backend-owned KYC record:

```json
{
  "provider": "sumsub",
  "status": "pending",
  "tier": 0,
  "level_name": "basic-kyc",
  "updated_at": "2026-08-20T15:00:00Z"
}
```

`status` is one of `not_started`, `pending`, `approved`, `rejected`, `on_hold`, or `expired`. Do not treat completion of the WebSDK as approval; reload this endpoint after the SDK closes and when rendering a refreshed session.

## Assets and balances

### Catalog

`GET /v1/assets/catalog`

```json
{
  "assets": [{ "code": "USDT", "display_name": "Tether", "status": "approved_pending_network_enablement" }],
  "networks": [{ "code": "ethereum", "display_name": "Ethereum", "status": "approved_pending_custody_enablement" }]
}
```

Catalog approval is not an enabled deposit or withdrawal route.

### Balances

`GET /v1/accounts/balances`

```json
{
  "balances": [{
    "account_id": "uuid",
    "account_kind": "funding",
    "account_name": "Funding Account",
    "asset_symbol": "USDT",
    "network": "ethereum",
    "available_atomic": "750000",
    "held_atomic": "0",
    "pending_atomic": "0",
    "locked_atomic": "0"
  }]
}
```

Balances only return assets with ledger activity. Empty accounts/assets are not returned. Do not create fake zero rows.

## Internal transfer

`POST /v1/transfers`

Required header:

```text
Idempotency-Key: <new-random-16-to-255-character-value>
```

```json
{
  "source_account_id": "uuid",
  "destination_account_id": "uuid",
  "asset_symbol": "USDT",
  "network": "ethereum",
  "amount_atomic": 250000
}
```

Success first request `201`, exact replay `200`:

```json
{ "transfer_id": "uuid", "journal_id": "uuid", "duplicate": false }
```

### Resolve a transfer recipient

`POST /v1/transfers/recipients/resolve`

```json
{ "type": "email", "value": "recipient@example.com" }
```

`type` is `uid`, `email`, or `phone`; phone values must be E.164. Success `200`:

```json
{ "uid": 123456, "destination_account_id": "uuid" }
```

Use the returned `destination_account_id` in `POST /v1/transfers`. On `404 recipient_not_found`, do not reveal extra recipient information. Phone lookup only resolves a verified phone number.

## Convert quotes

`POST /v1/conversions/quotes` creates a short-lived, Limiance-owned quote.
The backend uses configured market data only as an input; it applies the
operator-configured spread and fee using exact atomic-unit arithmetic. The
customer must never select a provider, market symbol, spread, or fee.

```json
{
  "source_account_id": "uuid",
  "from_asset_symbol": "BTC",
  "from_network": "bitcoin",
  "to_asset_symbol": "USDT",
  "to_network": "ethereum",
  "amount_atomic": 250000
}
```

Success `201`:

```json
{
  "quote_id": "uuid",
  "source_account_id": "uuid",
  "from_asset_symbol": "BTC",
  "from_network": "bitcoin",
  "to_asset_symbol": "USDT",
  "to_network": "ethereum",
  "input_amount_atomic": 250000,
  "output_amount_atomic": 123456,
  "fee_amount_atomic": 123,
  "price": "market-decimal-string",
  "provider": "bybit_public_spot",
  "expires_at": "2026-08-21T12:00:15Z"
}
```

Quotes are available only for individually enabled backend pairs. Do not show
mock conversion rates or infer a pair from the asset catalog.

### Confirm a conversion

`POST /v1/conversions/confirm` settles a still-valid quote atomically against
Limiance treasury. It does not place an external exchange order. It requires a
new `Idempotency-Key` for the confirmation action.

```json
{ "quote_id": "uuid" }
```

Success is `201` (or `200` for an exact idempotency replay):

```json
{
  "conversion_id": "uuid",
  "journal_id": "uuid",
  "status": "settled",
  "duplicate": false
}
```

Handle `409 conversion_quote_expired`, `409 conversion_quote_not_confirmable`,
and `409 conversion_insufficient_balance` without retrying automatically. A
successful settlement updates the customer ledger immediately. The backend may
reject a quote if Limiance treasury has insufficient configured inventory; do
not present success until this endpoint succeeds.

### Conversion history

`GET /v1/conversions?limit=25` returns real, newest-first settled conversions
with `id`, source/target asset and network, input/output/fee atomic values,
`status`, and `created_at`. Render an empty state when there are no records.

## Deposit address

`POST /v1/deposits/addresses`

```json
{ "asset_symbol": "USDT", "network": "ethereum" }
```

Success `200`:

```json
{
  "id": "uuid",
  "asset_symbol": "USDT",
  "network": "ethereum",
  "address": "on-chain-address",
  "tag": "",
  "provider_address_id": "provider-id",
  "status": "active"
}
```

Important:

- `409 deposit_route_unavailable`: no enabled/approved custody route; show unavailable.
- `503 custody_unavailable`: Fireblocks sandbox/production adapter not configured.
- A returned address does not mean the deposit is credited. Deposits are credited automatically after the required chain confirmations unless a backend risk rule places the deposit on hold. The customer UI must not offer or imply a manual deposit-approval action.

### Deposit history

`GET /v1/deposits?limit=25` returns up to 100 real deposit records, newest first:

```json
{
  "deposits": [{
    "id": "uuid",
    "asset_symbol": "USDT",
    "network": "ethereum",
    "amount_atomic": "250000",
    "transaction_hash": "chain-tx-hash",
    "confirmations": 12,
    "confirmations_required": 12,
    "status": "credited",
    "risk_status": "approved",
    "created_at": "2026-08-20T00:00:00Z",
    "updated_at": "2026-08-20T00:00:00Z"
  }]
}
```

Render an empty state when `deposits` is empty. Do not invent zero-value history rows.

## Withdrawals

`POST /v1/withdrawals`

Required header: `Idempotency-Key`.

```json
{
  "source_account_id": "uuid",
  "asset_symbol": "USDT",
  "network": "ethereum",
  "address": "destination-address",
  "tag": "",
  "amount_atomic": 250000
}
```

Success first request `201`, replay `200`:

```json
{
  "withdrawal_id": "uuid",
  "journal_id": "uuid",
  "duplicate": false,
  "status": "pending_approval"
}
```

This only creates a request and moves customer funds from `available` to `held`. It does not send funds on-chain. It needs an active, cooldown-complete whitelist address. The current backend still requires approved KYC; the planned KYC-tier withdrawal-limit policy has not yet been implemented, so do not claim that an unverified withdrawal allowance exists. The customer may cancel a pending request and submit Travel Rule data through the routes below. Approval remains an internal role-controlled process; on-chain submission and broadcast status are not yet available.

### Cancel a pending withdrawal

`POST /v1/withdrawals/{withdrawal_id}/cancel` cancels only a `pending_approval`
request. It atomically releases the held amount back to available through a new
immutable ledger journal. Success `200` returns the withdrawal ID, journal ID,
and `status: "cancelled"`. A request that is already approved, submitted, or
terminal returns `409 withdrawal_not_cancellable`.

### Travel Rule capture

`POST /v1/withdrawals/{withdrawal_id}/travel-rule` stores encrypted originator
(sender) and beneficiary (recipient) details for a `pending_approval`
withdrawal when the applicable Travel Rule policy requires it. The backend does
not yet return a `travel_rule_required` decision, so keep this form out of the
customer flow until that policy endpoint exists. This data is not returned
through customer history. Do not log this request body.

```json
{
  "originator_name": "Customer legal name",
  "originator_address": "Customer address",
  "beneficiary_name": "Beneficiary legal name",
  "beneficiary_address": "Beneficiary address",
  "beneficiary_country": "NG",
  "transfer_purpose": "Personal transfer"
}
```

### Withdrawal history

`GET /v1/withdrawals?limit=25` returns real requests, newest first, with `id`, `account_id`, `asset_symbol`, `network`, `address`, `tag`, `amount_atomic`, `status`, `created_at`, and `updated_at`.

### Withdrawal address book

- `GET /v1/withdrawal-addresses` returns `{ "addresses": [...] }`.
- `POST /v1/withdrawal-addresses` adds an address:

```json
{
  "asset_symbol": "USDT",
  "network": "ethereum",
  "address": "destination-address",
  "tag": "",
  "label": "My wallet"
}
```

The returned address has status `pending` until the backend-configured security cooldown expires; after that it becomes `active` and may be used for a withdrawal. Only individually enabled asset/network routes may be added.

- `DELETE /v1/withdrawal-addresses/{address_id}` disables, rather than erases, the address to preserve the financial audit trail.

## Notifications

- `GET /v1/notifications?limit=25` returns real, newest-first in-app
  notifications. Each record has `id`, `event_type`, `title`, `body`,
  `priority`, `metadata`, `read_at`, and `created_at`.
- `POST /v1/notifications/{notification_id}/read` marks one notification read.
- `DELETE /v1/notifications/{notification_id}/read` marks it unread.
- `POST /v1/notification-devices` registers an FCM or APNs device token:

```json
{ "platform": "fcm", "token": "provider-device-token" }
```

Registration persists the token but does not mean push delivery is enabled;
FCM/APNs provider credentials and delivery workers remain required.

## Operational safety

When withdrawals are temporarily disabled by authorised operations, new
withdrawal requests and approvals return `503 withdrawals_temporarily_disabled`.
The customer UI must show a neutral maintenance message and must not retry the
action automatically.

Conversions have an equivalent administrator-only kill switch. When it is off,
quote requests or confirmation return `503 conversions_temporarily_disabled`.
Show a neutral maintenance state and do not automatically retry.

## Implemented admin endpoint — not frontend customer UI

`POST /v1/admin/deposits/{deposit_id}/approve` is for a separate internal compliance app only. Do not link it from the customer frontend.

The internal admin application also has role-management routes. They require a
`platform_administrator` session and must never be surfaced in customer UI:

- `GET /v1/admin/users/{user_id}/roles`
- `PUT /v1/admin/users/{user_id}/roles/{role}`
- `DELETE /v1/admin/users/{user_id}/roles/{role}`

Allowed roles are `support`, `compliance`, `treasury_operator`,
`treasury_approver`, `auditor`, and `platform_administrator`. Changes are
audited and the final platform-administrator role cannot be removed.

## Backend features not yet available

Ask the client before building UI that needs any of these:

- Passkeys and secure account-recovery workflow
- Withdrawal approval quorum UI, Travel Rule trigger/policy, and on-chain broadcast status
- Convert history, treasury limits, and reconciliation
- Yellow Card and cNGN fiat flows
- Actual FCM/APNs delivery and security-email delivery templates
- Separate admin domain/session policy, audit search/export, and admin UI

## Required frontend acceptance checks

1. Run backend at `http://127.0.0.1:18080` and frontend through Vite.
2. Confirm browser sends cookies on every API call.
3. Register → verify email → login → session → logout works with real backend responses.
4. No mocked balance, transfer, deposit address, or withdrawal success is shown when the backend returns an error.
5. The same idempotency key replay shows the original operation, not a duplicate success animation.
6. No secrets, Sumsub token, Fireblocks values, OTP, or session information are written to browser storage or console.

# Limiance Staging Frontend API Handoff

This document describes the customer-facing API deployed to staging on 2026-08-23. It is for the frontend at `https://staging.celvios.site`.

## Connection contract

```text
API base URL: https://api.staging.celvios.site
Frontend origin: https://staging.celvios.site
Authentication: HttpOnly session cookie
```

Use one API helper for every request:

```ts
export async function api(path: string, init: RequestInit = {}) {
  return fetch(`https://api.staging.celvios.site${path}`, {
    ...init,
    credentials: 'include',
    headers: {
      'Content-Type': 'application/json',
      ...init.headers,
    },
  });
}
```

- Do not store session tokens in local storage, session storage, state persistence, or URL parameters. The browser owns the HttpOnly cookie.
- Do not test the authenticated session by opening the API URL directly in a different browser context. Call `GET /v1/auth/session` from the frontend helper instead.
- `401 {"error":"unauthenticated"}` at first application boot is normal. Render the signed-out state; do not show it as a system error.
- The API only permits the staging frontend origin. The old `limiance-main.vercel.app` address is intentionally not allowed by CORS.
- Treat all atomic amounts as strings. Never convert an atomic amount through JavaScript `Number`.

## Authentication and verification

| User action | Endpoint | Frontend behavior |
| --- | --- | --- |
| Register | `POST /v1/auth/register` | Body: `email`, `password`, `country_code`. On `202`, route to email verification. |
| Verify email | `POST /v1/auth/email/verify` | Body: `email`, `code` (six digits). On success, route to login. Verification does not create a session. |
| Resend verification | `POST /v1/auth/email/resend` | Body: `email`. Always show a neutral confirmation. |
| Login | `POST /v1/auth/login` | Body: `email`, `password`. On `200`, immediately call `/v1/auth/session` and then load the profile. |
| Login with TOTP | `POST /v1/auth/totp/verify` | Use after login returns `202 { status: "mfa_required", mfa_token }`. Body: `mfa_token`, `code`. |
| Logout | `POST /v1/auth/logout` | Clear client-only UI state after success; the API clears the cookie. |

### Unverified login behavior

A valid password for an unverified account returns:

```json
{
  "error": "email_verification_required",
  "verification_code_resent": true
}
```

The frontend must route to its email-verification page and prefill the email. Do not display this as a generic login failure.

## Profile, preferences, wallets, and history

### Profile

`GET /v1/user/profile`

Returns a durable profile plus the current ledger-calculated balances:

```json
{
  "profile": {
    "user_id": "uuid",
    "uid": 123456,
    "email": "customer@example.com",
    "display_name": "",
    "kyc_status": "not_started",
    "kyc_tier": 0,
    "preferred_currency": "USD",
    "preferred_language": "en",
    "preferred_theme": "system"
  },
  "balances": []
}
```

`PUT /v1/user/preferences`

```json
{
  "display_name": "Tolu",
  "preferred_currency": "USD",
  "preferred_language": "en",
  "preferred_theme": "dark"
}
```

Allowed themes: `system`, `light`, `dark`. Currency is a three-letter uppercase code. Do not save optimistic preferences locally before the API succeeds.

### Funding and Unified Trading Account balances

`GET /v1/wallet/balances`

```json
{
  "funding": [],
  "uta": [],
  "subaccounts": []
}
```

Each balance contains `account_id`, `account_kind`, `account_name`, `asset_symbol`, `network`, and `available_atomic`, `held_atomic`, `pending_atomic`, `locked_atomic` strings.

Use this endpoint for the wallet screen. `GET /v1/accounts/balances` remains available for compatibility but returns one flat `balances` array.

### Immutable transaction history

`GET /v1/accounts/transactions?account_kind=funding&limit=50&cursor=123`

- `account_kind` is optional: `funding`, `uta`, or `subaccount`.
- `limit` is 1–100, default 50.
- Pass `next_cursor` from the prior response for the next page.
- A transaction record is a posted ledger entry and includes `journal_id`, `reference_type`, `reference_id`, account/asset metadata, `bucket`, `direction`, `amount_atomic`, and `occurred_at`.

Render the server response as the source of truth. A `credit`/`debit` direction is a ledger posting direction, not a display-ready “received/sent” label; derive UI wording from `reference_type` and the account context.

## Security

| Feature | Endpoint | Body / result |
| --- | --- | --- |
| List sessions | `GET /v1/user/sessions` | Returns `sessions`. `GET /v1/security/sessions` is equivalent. |
| Revoke session | `DELETE /v1/user/sessions/{session_id}` | If it is the current session, send the user to signed-out state. |
| Enrol TOTP | `POST /v1/security/totp/enroll` then `POST /v1/security/totp/confirm` | Confirm body: `{ "code": "123456" }`. Do not persist the TOTP secret after the enrollment screen closes. |
| Step-up | `POST /v1/auth/mfa/step-up` | `{ "purpose": "withdrawal_address", "code": "123456" }`; returns a five-minute `step_up_token`. Do not store it persistently. |
| MFA recovery | `POST /v1/auth/mfa/recover` | `{ "email": "…", "reason": "I lost access to my authenticator" }`. This only creates an audited human-review request; it does not reset MFA automatically. |
| Password-reset email | `POST /v1/auth/password-reset/request` | `{ "email": "…" }`; always render a neutral “if the account exists” confirmation. |
| Set new password | `POST /v1/auth/password-reset/confirm` | `{ "email": "…", "code": "123456", "new_password": "…" }`. Success revokes all existing sessions. |
| Push token registration | `POST /v1/notifications/device-token` | Existing equivalent: `POST /v1/notification-devices`. Send platform/token only after the customer grants device permission. |

Use a narrow, stable purpose string for step-up, for example `withdrawal_address`, `withdrawal_request`, or `security_change`. Its result is for the immediate sensitive request only; never use it as a replacement for the session cookie.

## Deposits

### Address generation

`POST /v1/wallet/deposit-addresses` (equivalent to `POST /v1/deposits/addresses`)

```json
{ "asset_symbol": "ETH", "network": "ethereum_sepolia" }
```

The deployed sandbox catalog currently supports these routes:

| Asset | Network code |
| --- | --- |
| BTC | `bitcoin_testnet4` |
| ETH | `ethereum_sepolia`, `arbitrum_sepolia`, `base_sepolia`, `optimism_sepolia` |
| USDC | `ethereum_sepolia` |

The backend also accepts common hyphenated UI names such as `ethereum-sepolia` and normalizes them to the catalog code. These are testnet routes only.

Handle errors as follows:

- `401`: session has expired or was not established; refresh auth state.
- `409 deposit_route_unavailable` or `custody_network_not_approved`: disable that asset/network selector; do not retry automatically.
- `503 custody_unavailable`: show a temporary provider-unavailable state and permit a manual retry.

`GET /v1/deposits` returns the customer’s deposit history. A confirmed, unflagged deposit is credited by the backend; there is no normal frontend “approve deposit” action.

## Withdrawals: current UI boundary

Existing customer endpoints:

- `POST` / `GET /v1/wallet/withdrawals` (aliases for `/v1/withdrawals`)
- `POST /v1/withdrawals/{withdrawal_id}/cancel`
- `POST /v1/withdrawals/{withdrawal_id}/travel-rule`
- `GET` / `POST` / `DELETE /v1/withdrawal-addresses`

The backend currently holds funds and creates a `pending_approval` withdrawal record. Fireblocks broadcast, provider-status reconciliation, risk-policy decisioning, and approval quorum APIs are **not yet implemented**.

Therefore:

- Render the exact status returned by the server.
- Do not claim “sent”, show a transaction hash, or display a blockchain confirmation timeline for a withdrawal unless the backend has returned that state.
- Keep “pending approval” explanatory and allow cancellation only when the API allows it.
- Do not build a frontend-only risk approval, broadcast, or maker-checker button.

## Notifications

- `GET /v1/notifications`
- `POST /v1/notifications/{notification_id}/read`
- `DELETE /v1/notifications/{notification_id}/read`

Email verification and password-reset emails are live through the staging worker pipeline. Device-token registration persists a device token, but actual FCM/APNs push delivery is not yet enabled; label it accordingly in product UI.

## Still unavailable — keep disabled or hidden

Do not manufacture these workflows in the frontend:

1. Subaccount creation and subaccount management.
2. Funding ↔ UTA internal transfer controls.
3. Fireblocks withdrawal broadcast/reconciliation and policy/quorum approvals.
4. Automatic MFA-recovery decisions; recovery is staff-reviewed.
5. Live FCM/APNs push sending.
6. Spot trading, order management, market WebSockets, API keys, and fee tiers.
7. Separate staff/admin authentication and admin operational queues.

## Recommended frontend integration order

1. Point staging to the API helper above; use `credentials: 'include'` on every request.
2. Implement registration, verification redirect on `email_verification_required`, login, session bootstrap, and logout.
3. Build profile/preferences, Funding/UTA wallet, and cursor-based transaction history from server data.
4. Add password reset, TOTP enrollment, session management, step-up prompts, and MFA recovery request UI.
5. Integrate testnet deposit address generation and deposit history with the stated error handling.
6. Keep withdrawal lifecycle beyond `pending_approval` visibly restricted until the corresponding backend work ships.

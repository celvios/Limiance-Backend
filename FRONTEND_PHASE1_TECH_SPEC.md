# Limiance Phase 1 Frontend Technical Specification

`FRONTEND_API_HANDOFF.md` is the API contract. This document defines the
customer web application's required behaviour and boundaries for Phase 1.

## 1. Application boundary

- Vite configuration: `VITE_API_BASE_URL=http://127.0.0.1:18080` in development.
- All requests use `credentials: 'include'`; the session is an HttpOnly cookie.
- Never save a session, OTP, TOTP secret, MFA token, Sumsub token, private key,
  Travel Rule details, or provider response in browser storage or console logs.
- Use atomic integer strings/numbers supplied by the API. Never calculate money
  with JavaScript floating point values. Do not add display decimals until the
  backend supplies asset decimal metadata in the required route.
- State-changing money actions use a new UUID `Idempotency-Key`. Reuse it only
  to retry the same submitted action after a network failure.
- A `401` means clear in-memory customer state and route to Login. A `403
  account_frozen` routes to Support/Recovery.
- Display neutral errors. Do not render raw API/provider error strings.

## 2. Shared client and application state

Implement one API module. It must parse a JSON body safely, include JSON
content type where a body is sent, include credentials, and attach the
idempotency header only for the action being performed.

The only persistent browser preference permitted in Phase 1 is non-sensitive
presentation state such as theme. Customer identity is loaded from
`GET /v1/auth/session` at app boot and kept in memory.

Routes requiring authentication must wait for the session check. They must
render a loading state, then redirect to Login only if the backend returns 401.

## 3. Required customer screens

### Authentication

- **Register:** email, password, country code. Enforce 12–128 characters with
  uppercase, lowercase, and a digit. Success routes to email verification.
- **Email verification:** email and six-digit code; invalid/expired code leaves
  the form editable. Resend uses `POST /v1/auth/email/resend` and always shows
  the neutral response specified in the handoff.
- **Login:** email and password only. If it returns `mfa_required`, retain the
  returned MFA token in component memory only and route to the TOTP-code screen.
- **TOTP code:** on a failed code, discard the token and return to password
  login. The “Lost authenticator access?” link may be visible but must route to
  Support; it must not imply self-service recovery because no recovery API exists.
- **Logout:** calls the API, clears memory state, then routes to Login.

### Security and account settings

- **TOTP enrollment:** call enroll, create a QR from `otpauth_uri` in memory,
  ask for confirmation code, then discard URI and secret. Include Disable TOTP
  with a current code.
- **Active sessions:** list `GET /v1/security/sessions`; revoke selected
  sessions. If the API says the current session was revoked, go to Login.
- **Phone binding:** E.164 phone entry, Start and Verify states, plus clear
  messages for invalid code, number already used, and provider temporary error.
- **Anti-phishing code:** input accepts only the documented 4–32-character
  uppercase format; an empty value clears it. Do not attempt to read it back.
- **Freeze account:** explicit destructive confirmation modal. On success clear
  state and route to Support/Recovery. There is no Unfreeze button.

### KYC

Show the durable `GET /v1/kyc/status` state on load. The Start Verification
button calls `POST /v1/kyc/sessions`. The returned token can be passed directly
to the approved Sumsub WebSDK integration and must be discarded immediately
after the SDK closes. Refresh KYC status after close. Do not report approved
until the status API reports `approved`.

Adding the official Sumsub package needs explicit product approval before the
dependency is added. Until then, make the CTA explain that verification is not
available; do not simulate the SDK.

### Wallet, balances, deposits, and transfers

- Use `GET /v1/accounts/balances` exactly as returned. Render an empty state
  rather than fake zero balances.
- Build deposit-address generation from `POST /v1/deposits/addresses` and show
  its asynchronous-credit warning. Deposit history is `GET /v1/deposits`.
- Internal transfer first resolves email, UID, or verified E.164 phone with
  `POST /v1/transfers/recipients/resolve`; submit only with the returned account
  ID. Never guess recipient IDs.
- Transfer creation, deposit address, and withdrawal failures must not show a
  success animation.

### Withdrawals

- Address book: list, add, and disable addresses. Show `pending` cooldown state
  as unavailable for withdrawal.
- Withdrawal form accepts only an active address and uses a new idempotency key.
  The resulting `pending_approval` state means funds are held, not broadcast.
- History has cancel action only for `pending_approval` records. Refresh history
  after a cancellation result.
- The Travel Rule form is **not automatic**. Do not show it until a future
  backend trigger/policy field states it is required. If Operations asks for it,
  submit only through the documented route and never persist its form data.

### Convert

The Convert page has two explicit stages.

1. **Quote:** asset/network source and target, owned source account, and atomic
   amount. Call `POST /v1/conversions/quotes`. Display input amount, output
   amount, fee, price, provider label, and an expiry countdown based on
   `expires_at`. Do not let the frontend choose market symbol, price, spread,
   or fee.
2. **Confirm:** only while the quote remains current. Send exactly
   `{ "quote_id": "..." }` to `POST /v1/conversions/confirm` with one
   idempotency key. Disable duplicate clicks while pending. On timeout, retry
   with the same key; otherwise fetch `GET /v1/conversions` before claiming a
   result.

Handle quote expiry, insufficient inventory/balance, disabled-conversion
maintenance, and unavailable quote service visibly and safely. A successful
confirmation is already settled in the internal ledger—do not show a separate
“processing trade” status. Render `GET /v1/conversions?limit=25` as the real
conversion history; no invented rows or estimated P&L.

### Notifications

Render the in-app list, read/unread controls, and empty state from the API.
Device registration may call `POST /v1/notification-devices` after the browser
has obtained a real FCM/APNs token. Registration does not mean push delivery is
configured, so do not promise notifications until the provider worker exists.

## 4. Deliberately excluded from the customer frontend

- Admin roles, deposit approval, withdrawal approvals, conversion pair policy,
  and conversion/withdrawal kill switches are internal administrator routes.
- Passkeys, trusted-device claims, customer self-service account recovery,
  Fireblocks broadcast state, fiat on/off-ramps, and security-alert push/email
  delivery are not Phase 1 customer features.
- Do not create screens or mock data for missing routes.

## 5. Acceptance checklist

1. Register → verify → login → session → logout with the local backend.
2. Verify cookies are sent for every API request in browser developer tools.
3. Confirm balances, deposit/withdrawal history, and convert history never use
   fixture financial values.
4. Confirm every monetary POST sends an idempotency key and exact replay is
   rendered as the original action, not a second success.
5. Confirm browser storage and console contain no session, secrets, KYC token,
   TOTP secret, OTP, or Travel Rule data.
6. Exercise error paths: unauthenticated, frozen, quote expired, conversion
   maintenance, insufficient balance, address cooldown, and withdrawal pending
   approval.

# Phase 1 — Remaining Work: Items 4–6

This is the implementation backlog after the current customer API foundation.
Entries are marked by their actual backend state; the frontend must not claim a
feature is live solely because a screen exists.

## Authentication and security

- **Phone verification UI — backend ready; frontend required.** Customer API
  routes are `POST /v1/auth/phone/start` and `POST /v1/auth/phone/verify`.
  Build the authenticated Account Page phone-binding flow using these routes.
- **Account freeze — backend ready; frontend required.** Add an emergency
  `POST /v1/auth/freeze` action in Account/Security settings. It signs the
  customer out everywhere and must route them to support/recovery. Do not build
  a self-service unfreeze control.
- **TOTP — backend ready; frontend required.** Implement authenticator-app
  enrollment, confirmation, the `mfa_required` login branch, and disable flow
  exactly as documented in `FRONTEND_API_HANDOFF.md`. Never persist TOTP
  secrets, QR payloads, or MFA challenge tokens in the browser.
- **Active-session management — backend ready; frontend required.** Render
  `GET /v1/security/sessions` and allow a customer to revoke individual active
  sessions with `DELETE /v1/security/sessions/{session_id}`. Do not create an
  opaque client-side device fingerprint.
- **Anti-phishing code — backend ready; frontend required.** Provide a
  `PUT /v1/security/anti-phishing-code` setting. The code is saved but is not
  yet rendered into every security-email type; do not overstate its coverage.
- **Advanced security — partially complete.** TOTP is enforced at login,
  account freeze revokes all sessions, and active-session management is ready.
  Still required: passkeys, anti-phishing code, password recovery, security
  preference APIs, and actual email/push delivery of security alerts.

## Money movement

### Completed withdrawal cancellation

The customer cancellation endpoint accepts only pending approval withdrawals. It
posts an immutable held-to-available release journal and creates audit and
outbox records. Any approved, submitted, or terminal withdrawal is rejected.

- **Withdrawal cancellation — backend complete; frontend required.** Only
  pending withdrawal requests may be cancelled; cancellation atomically
  releases held funds back to available and records audit/outbox events.
- **Withdrawal approval quorum — partially complete.** The backend records
  reasons and requires two distinct `treasury_approver` users before status
  becomes `approved`. Remaining: provision the three-person approver roster,
  admin review/list APIs, and operational UI.
- **Travel Rule capture — partially complete.** Required data is captured and
  encrypted at rest. Remaining: provider/corridor policy and controlled
  submission after the withdrawal approval workflow.
- **Fireblocks broadcast/status — backend required.** Add a submission command
  after quorum, persist the provider transaction ID, consume signed Fireblocks
  transaction webhooks, expose customer-safe status, and add a kill switch.

## Fireblocks Sandbox verification

- **ETH Sepolia route enabled locally:** `ETH` / `ethereum-sepolia` /
  `ETH_TEST5`, one confirmation, Sandbox only.
- **Still required:** an authenticated Limiance test-user session to invoke
  `POST /v1/deposits/addresses`; this route creates that user's Fireblocks
  custody vault/address. Never bypass the session or create an orphan vault
  merely to test credentials.
- **Treasury source:** Fireblocks Sandbox vault `1`, used later for withdrawals
  and conversion treasury, never as a customer deposit address.

## Trading and fiat

- **Convert quotes — backend ready; frontend required.** The customer route is
  `POST /v1/conversions/quotes`. It only works for administrator-enabled pairs
  and creates a 15-second Limiance-owned quote using exact atomic-unit
  arithmetic. Market data is read-only input, never trade execution. Quote
  confirmation is idempotent and posts an atomic internal settlement journal.
  Remaining: conversion history, treasury limits, and reconciliation.
- **Fiat flows — provider access required.** Yellow Card and cNGN adapters,
  webhooks, corridors, limits, reconciliation, and commercial/issuer approval.

## Communication

- **In-app notifications — partially complete.** Paginated list, read/unread
  state, device registration, and priority persistence are ready. Actual
  FCM/APNs delivery, retries, and provider failure handling remain.

## Administration

- **Admin authentication and role management — partially complete.** Existing
  authenticated users with the `platform_administrator` role can list, grant,
  and revoke internal roles; every change is audited and the final administrator
  cannot be removed. Remaining: separate admin domain/session policy,
  bootstrap procedure, audit-log search/export, compliance/KYC review,
  treasury operations, and auditor UI.

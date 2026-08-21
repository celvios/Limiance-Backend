# Limiance Admin Frontend API and Operations Handoff

This is the internal operations console specification. It is not part of the customer application. Production must use a separate admin origin and staff-session policy.

## Operating model

- Confirmed, unflagged deposits are credited automatically; admins work only deposit exceptions.
- Sumsub is the KYC document store and review provider. Staff review provider exceptions, holds, and escalations.
- Withdrawals are policy scored, held atomically, then either automatically eligible or routed to compliance/treasury approval.
- Every staff decision requires a reason and creates an immutable audit event.

## Mandatory frontend controls

- Use `credentials: 'include'`; never store staff sessions in browser storage.
- Require step-up authentication for privileged staff actions.
- Hide actions unless the backend authorizes the staff role and current case state.
- Never log or persist KYC documents, short-lived document URLs, Travel Rule data, custody/provider responses, or raw audit data.
- Confirm and collect a meaningful reason before any approval, rejection, policy change, or kill-switch change.

## Roles

| Role | Use |
| --- | --- |
| `support` | Customer/case lookup only; no fund or KYC decisions |
| `compliance` | KYC and risk exceptions |
| `treasury_operator` | Prepare custody operations |
| `treasury_approver` | Four-eyes approval of treasury actions |
| `auditor` | Read-only audit/reconciliation |
| `platform_administrator` | Roles and operational controls |

The backend already prevents removal of the final `platform_administrator`.

## KYC review and documents

### Required flow

```text
Customer submits documents in Sumsub WebSDK
  -> Sumsub stores documents and reviews them
  -> verified Sumsub webhook updates Limiance KYC state
  -> on-hold/rejected/escalated case appears in admin queue
  -> authorised compliance user opens a short-lived provider-backed document viewer
  -> staff decision is audited and synchronised with Sumsub
```

Yes: the compliance admin needs to see KYC documents when a case requires review. The browser must receive only a short-lived backend-authorised viewer URL (or a proxied stream). It must never call Sumsub with a secret, and Limiance should not copy raw ID documents into its ordinary database.

### Current backend state

The backend stores provider, applicant ID, KYC status, tier, and review timestamps. Sumsub webhooks update the status. It does **not** currently expose an admin KYC queue, applicant data, documents, or a manual KYC decision action. Therefore the frontend cannot currently approve KYC or show documents.

### Required endpoints

```text
GET  /v1/admin/kyc/cases?status=on_hold&cursor=...
GET  /v1/admin/kyc/cases/{case_id}
POST /v1/admin/kyc/cases/{case_id}/document-view
POST /v1/admin/kyc/cases/{case_id}/decision
```

`document-view` must require `compliance`, create an audit event, and return a single-use/short-lived URL plus expiry. It must not return provider secrets.

Decision body:

```json
{ "decision": "approve", "tier": 1, "reason": "Document and liveness review completed" }
```

Allowed decisions: `approve`, `reject`, `hold`, and `request_resubmission`. The backend must enforce state transitions and synchronise with Sumsub before the UI claims final approval.

## Deposit exceptions

Do not build a normal deposit-approval queue. Only show reorged, failed, unmatched, wrong-memo, sanctions/risk-held, or policy-held deposits.

Required endpoints:

```text
GET  /v1/admin/deposits?status=pending_risk_review&cursor=...
GET  /v1/admin/deposits/{deposit_id}
POST /v1/admin/deposits/{deposit_id}/release
POST /v1/admin/deposits/{deposit_id}/reject
```

The existing `POST /v1/admin/deposits/{deposit_id}/approve` is a legacy exception action, not a normal deposit workflow.

## Withdrawal operations

```text
Request -> balance/address/KYC-tier/value/Travel Rule/risk checks -> hold funds
 -> policy approval or compliance/treasury queue -> distinct approver(s)
 -> Fireblocks broadcast -> chain/provider reconciliation -> final status
```

Admin screens need a filtered queue, case detail, exact atomic amount, locked USD-equivalent valuation and timestamp, recipient/address risk, Travel Rule completeness, audit timeline, approval/reject/escalate actions, and Fireblocks broadcast state.

Current action:

```text
POST /v1/admin/withdrawals/{withdrawal_id}/approvals
{ "reason": "reason of at least 8 characters" }
```

The queue, KYC-tier limits, Travel Rule policy decision, Fireblocks broadcast, and broadcast-status endpoints are still required.

## Policies and controls

The policy page must version and audit:

- KYC-tier limits: initial non-KYC default is 20,000 USDT/day and 100,000 USDT/month.
- Per-chain confirmations, deposit minimums, and credit limits.
- Travel Rule threshold/country/counterparty rules.
- Automatic approval, high-value maker-checker, and quorum thresholds.
- Address cooldowns, velocity, and blockchain-risk policy.
- Enabled assets/networks and provider mappings.

A withdrawal must retain the exact policy version and valuation used for its decision.

Existing administrator controls:

```text
GET/PUT /v1/admin/operational-controls/withdrawals
GET/PUT /v1/admin/operational-controls/conversions
PUT     /v1/admin/conversion-pairs
GET     /v1/admin/users/{user_id}/roles
PUT     /v1/admin/users/{user_id}/roles/{role}
DELETE  /v1/admin/users/{user_id}/roles/{role}
```

## Audit and reconciliation

Required read-only pages: audit-event search, journal/posting explorer, custody-vs-ledger reconciliation, webhook delivery/processing status, and controlled exports. These require dedicated backend endpoints and server-side pagination.

## Delivery order

1. Separate admin session/domain and staff authentication.
2. KYC case queue, provider-backed document viewer, audited decisions.
3. Withdrawal queue, KYC-tier limit/valuation policy, Travel Rule decision, and Fireblocks lifecycle.
4. Deposit exception queue and risk integration.
5. Versioned policy configuration.
6. Audit, reconciliation, and exports.

Never build a visual-only admin button for a money-moving action; every control must map to a backend-authorised transition.

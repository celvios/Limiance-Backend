# Staging test-money implementation plan

Date: 2026-09-05. Issuance core implemented and tested; integration and activation pending.

## Approved policy

- Named testers receive individually proposed, independently approved grants.
- User specified 10,000 USDT worth of tokens. Plan interprets this as an
  aggregate grant ceiling per tester, not a per-token allowance. Token selection,
  valuation rules and replenishment remain to be finalized before issuance.
- Issued money may trade with deposited test balances through real matching.
- Withdrawals are limited to net confirmed deposits of the same token on the same
  network. Bought-back tokens may be withdrawn up to that limit. Issuance,
  trading proceeds and profits create no additional withdrawal entitlement.
- Withdrawal admission requires remaining deposit entitlement AND available
  same-asset balance AND actual custody availability, including fee requirements.
- Reject insufficient custody before broadcast. Release reservations only after
  confirmed non-submission; retain them while a provider outcome is unknown.
- No blanket withdrawal shutdown. Preserve legitimate deposits and withdrawals.

## Architecture and scope

One Go monolith owns PostgreSQL; separate C++ engine owns its append-only journal.
Keep ZeroMQ, frozen FlatBuffers and /v2 trading contracts. Use integer atomic units,
balanced append-only journals, outbox/inbox, idempotency, sorted balance locks and
maker-checker. No fabricated deposits, backing, trades, volume, candles or profits.
No test balances migrate into mainnet. Payment processing and derivatives remain
deferred. No extra frontend warning banner. Completion of this plan funds nothing.

## Inspection evidence

- Backend baseline 0fc42c85; modified REFERENCE_PRICE_PLAN.md and untracked
  package-lock.json preserved. Frontend baseline 7f9025d.
- AWS authenticated account 041659147758 works outside sandbox. Fresh ECS read:
  API117 2/2, market-data3 1/1, matcher1 1/1, settlement2 1/1, deposits7 1/1,
  withdrawals35 1/1; all COMPLETED with no pending tasks.
- Historical audit dc649ff73f374ed2ad2ef7d8e93d6132 recovered: applied=false,
  funded_system_accounts=[], active_pairs=[]. Not current role/balance evidence.
- internal/marketmaker/activation.go has distinct roles, request hashes, audit,
  outbox and inventory journals. applyDryRun uses an activation-wide lock rather
  than shared account/asset locks; concurrent treasury debits need verification.
- internal/ledger/account_lock.go defines shared locking; trading settlement
  sorts locks and converts pair/asset scales. Core amounts use NUMERIC(78,0).
- migrations/000063_spot_market_catalog.up.sql separates internal_spot asset IDs
  from custody routes. Equal symbols are not implicit conversion authorization.
- Customer deposits credit Funding. ApprovedWithdrawals selects the customer's
  custody vault. Issued/trading balances do not establish tokens in that vault.
- custody.Provider lacks capacity reads. withdrawals.Worker has no explicit
  capacity reservation; its query locks end before external submission. Assess
  durable claims, concurrency and unknown-outcome recovery before extension.
- Staging RoutePolicy currently allows bitcoin_testnet4 and ethereum_sepolia.
  Catalog entries do not prove signing authorization or mainnet isolation.
- Current withdrawal request path checks available balance but does not maintain
  the newly required deposited-token entitlement. This model is not implemented.

## Entitlement design

Use an immutable entitlement event stream keyed by user and exact custody asset
identity (including network), separate from spendable ledger balances. Create
entitlement only after unique confirmed deposit credit. Never derive it from a
balance snapshot, grant, conversion or trade event. Record provider transaction
and event identity; replay creates no second entitlement.

Withdrawal hold reserves entitlement and ledger funds atomically. Completion
consumes the reservation once; confirmed failure releases it once. Concurrent
withdrawals cannot reserve the same entitlement or custody capacity twice.
Reorg/reversal handling must reduce entitlement or block fulfillment without
editing historical events. Backfill only from verified credited deposit records,
deducting completed and outstanding withdrawals; ambiguous history fails closed.

User confirmed bought-back tokens may be withdrawn up to net deposits. Trading
does not consume the deposit ceiling or replenish it. Remaining entitlement is
confirmed credited deposits less completed withdrawals, active withdrawal
reservations and applicable deposit reversals. Track this by user, token and
network; do not follow individual token lots or transfer entitlement between users.
Same-owner Funding/UTA transfers retain the user's ceiling; they create no new
entitlement. Test subaccount ownership and explicit bridge conversions without
silently moving an entitlement to another token or network.

## Ordered implementation checklist

Completed task: isolated default-off customer issuance service, policy and
database tests. No HTTP wiring or activation until entitlement/isolation gates
pass. Outstanding audit, frontend, custody, lifecycle and release tasks remain open.

- [x] Inspect authoritative code/contracts and preserve worktree.
- [x] Verify AWS access, six service rollouts and historical audit report.
- [x] Record approved grant/withdrawal policy and this engineering plan.
- [ ] Audit current DB roles, controls, balances, deposits, withdrawals, route
  mappings and downstream command cessation using reviewed read-only queries.
- [ ] Build default-off staging issuance: non-spendable counterpart accounts,
  balanced journals, versioned limits, recipient/reason, proposal/approval roles,
  payload-bound idempotency, sorted locks, atomic quota use and audit/outbox.
- [x] Implement isolated customer grant policy/service and immutable history,
  with real PostgreSQL transaction, concurrency, rollback and idempotency tests.
- [ ] Complete issuance integration: trusted independent-reference adapter,
  reviewed policy/recipient administration, read/proposal/approval API contracts,
  treasury-specific allocation limits and runtime wiring after safety gates.
- [ ] Enforce test/mainnet isolation before enabling issuance: deployment/DB
  identity, custody workspace/source, chain verification and signing allowlist.
  Reject mismatched startup/configuration and prohibit production import.
- [ ] Establish deposit entitlement and withdrawal enforcement before issued
  funds can reach an existing withdrawal path, including bought-back-token tests.
- [ ] Issue bounded approved treasury inventory, then allocate through audited
  maker activation with shared sorted locks. No synthesized deposit events.
- [ ] Implement any explicit custody/internal_spot bridge: journals balanced
  within each asset, exact scale/dust policy, provenance and compensations.
  No implicit same-symbol mapping or new entitlement from conversion.
- [ ] Configure selected pairs with two fresh reference venues, atomic exposure
  limits, stale/divergence protections and dry-run evidence. Prove stop command
  cessation and outstanding-order handling before distinct release approval.
- [ ] Complete frontend orders/cancel, available/held and withdrawable balances,
  fees/fills/history and defined execution-derived P&L, using strings/BigInt.
- [ ] Prove real Go -> C++ -> settlement execution and replay correctness.
- [ ] Prove deposit-only withdrawals, custody capacity/fees, failure recovery
  and concurrent reservation behavior without disabling legitimate withdrawals.
- [ ] Reconcile ledger per journal/asset separately from issued liabilities,
  deposit entitlements, reservations and actual custody holdings.
- [ ] Complete browser/mobile flows, load/wire evidence and Section 9 sign-off.

## Proposed files, schema and test evidence

Paths are relative to C:/Users/toluk/Desktop/Limiance Backend.
Finalize each task's exact scope before implementation.

- New internal/testmoney/policy.go, service.go, postgres.go and matching tests.
- New migrations/000068_staging_test_money.up.sql and companion down migration
  after confirming numbering. Tables: versioned policies, recipients, requests
  and grant-to-journal links. Default enables no issuance; rollback cannot erase
  executed journal history. Entitlement events/reservations get a separate
  domain migration using the confirmed net-deposit ceiling policy.
- New internal/platform/httpserver/test_money.go and test_money_test.go.
  Proposed POST /v2/admin/test-money/issuances, POST
  /v2/admin/test-money/issuances/{request_id}/approve and GET detail route.
  These are not deployed; both mutations need idempotency and role enforcement.
- internal/marketmaker/activation.go and activation_test.go need shared locks
  and PostgreSQL allocation-concurrency coverage before funding.
- Likely integration files: internal/config/config.go,
  internal/platform/httpserver/server.go, internal/datamanager/manager.go,
  internal/custody/provider.go, internal/custody/policy.go,
  internal/custody/fireblocks.go, internal/withdrawals/worker.go,
  cmd/withdrawals/main.go and openapi/exchange-v2.yaml. Present exact changes
  to working foundation code for approval before modifying it.
- Frontend files under C:/Users/toluk/Desktop/limiance require component
  inspection before selecting paths. No frontend implementation in this task.

Build tests alongside each feature. Issuance: missing/revoked roles, distinct
approver, eligibility, exact valuation, concurrent limits, duplicate/conflicting
keys, rollback, audit/outbox, production and mainnet rejection.

Entitlement: issuance-only withdrawal denied; confirmed same-token/network
deposit allowed; different token/network denied; trades/profits/transfers never
mint entitlement; bought-back tokens remain eligible within the ceiling. Cover partial and
concurrent withdrawals, duplicate deposits, backfill, reorg, failed/unknown
submission, cancellation and terminal replay. Custody tests cover actual
capacity and fees, durable claims, stable provider IDs and crash recovery.
Actual testnet deposit/withdrawal evidence remains separate from mocks.

Execution tests map to supplied 3.1-3.4 and 4.2-4.3; market data to 3.5 and 5;
fees/replay to 6; auth/idempotency/rate limits to 7; load to 8; recording and
sign-off to 9. Issuance/entitlement tests supplement these. Validate stale sample
endpoints/arithmetic against code/OpenAPI. Skips do not count as passes.

Each implementation task reports full paths, SQL, core logic, proving tests,
mapping and limitations, then receives a domain-named commit. Preserve existing
handoff edits and package-lock.json. No push/deployment for the isolated core.

## Issuance core evidence (2026-09-05)

Created files under C:/Users/toluk/Desktop/Limiance Backend:

- internal/testmoney/doc.go
- internal/testmoney/policy.go
- internal/testmoney/service.go
- internal/testmoney/postgres.go
- internal/testmoney/policy_test.go
- internal/testmoney/postgres_test.go
- migrations/000068_staging_test_money.up.sql
- migrations/000068_staging_test_money.down.sql

Modified this plan only among existing tracked files.
Migration adds policies, a disabled control row, named recipients, immutable
requests and immutable approved grants. Request and grant history cannot be
updated/deleted; rollback refuses to erase recorded requests. No policy/recipient
is enabled by migration. No deployment route imports the service.

Core: proposal hashes bind actor/key to payload. Approval rechecks both active
roles, different actors, named recipient/account/asset eligibility and the exact
policy version. The administrative control-row lock serializes quota consumption.
Grant valuation uses two distinct fresh sources and big.Int ceiling arithmetic.
Lifetime issued USDT totals span all assets, accounts and policy versions.
Sorted shared ledger locks protect posting. One transaction inserts the balanced
journal, frozen unowned counterpart, grant/evidence, audit and outbox; it creates
no deposit or withdrawal entitlement.

Validation: go test ./... -count=1 passed with LIMIANCE_TEST_DATABASE_URL pointed
at disposable local PostgreSQL, after applying all repository migrations there.
All 12 new top-level issuance tests passed (4 unit, 8 database). Database cases
cover disabled/environment mismatch, exact posting and retry, revoked roles,
same checker, disabled eligibility, policy change, concurrent quota contention,
outbox-failure rollback, conflicting approval keys, cross-token/version quotas
and global quotas across recipients. The eight database tests create isolated
schemas from actual migrations. External integrations without their configured
dependencies may still skip in the broad suite; no staging or Section 9 claim.

Remaining: runtime/database/custody isolation proof, deposited-token entitlement,
custody-capacity enforcement, audited treasury funding, reference adapter, HTTP
integration, current staging audit, frontend and real deployed execution gates.
The named-user 10,000-USDT ceiling is implemented without automatic replenishment.
No global cap, asset list, reference tolerances or treasury limits are activated.

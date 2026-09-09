# Staging test-money implementation plan

Date: 2026-09-09. Issuance and withdrawal controls are tested; the bounded staging pilot is being activated.

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

Completed task: custody observation primitives. Add exact provider-balance and
medium-fee reads plus external-ID transaction lookup to the Fireblocks adapter.
All monetary responses remain strings and are converted to bounded atomic units
without float64. Missing/invalid fee or balance data fails closed. A not-found
lookup is evidence for reconciliation only and never releases or rearms a dispatch.
Files: internal/custody/provider.go, fireblocks.go, fireblocks_test.go and this plan.

Completed task: approved custody capacity routes and atomic reservations. Added
immutable route definitions with separate proposal/approval records, explicit
withdrawal-token and native-fee-token mappings, bounded observation freshness,
per-transaction fee caps and default-off activation. Before custody is called,
lock both provider vault/asset capacity keys in sorted order, subtract every
unresolved local reservation and atomically persist token/gas reservation with
the dispatch, audit and outbox. No route is seeded or enabled by migration.
Files: migration 000071 custody capacity up/down, datamanager capacity/dispatch,
withdrawal worker and PostgreSQL/unit tests, plus this plan.

Completed task: provider reconciliation for submitted and unknown dispatches.
Use the stable external withdrawal ID and fresh provider observations to record
an immutable consumed or safely released capacity outcome. A timeout, missing
ACK or single not-found response never releases capacity or rearms submission.
Provider-confirmed completed/failed outcomes close capacity atomically with the
ledger transition. Successful not-found observations are retained and rate-limited
but never auto-release funds. Provider errors create no evidence or state change.

Completed task: pre-rollout legacy dispatch audit and controlled staging release.
Every dispatch created before capacity reservations was reconciled by stable
external ID without resubmission. API revision 118 and withdrawal revision 36 now
run the immutable image digest recorded below. Route mappings/fee caps remain
default-off and require separate approval before any capacity-enforced route is
enabled. Genuine deposit-withdrawal browser evidence remains outstanding.

Fresh staging evidence on 2026-09-06: Fireblocks sandbox external-ID lookups
reported the three legacy withdrawals COMPLETED with distinct provider IDs and
transaction hashes. Controlled reconciliation task
eed4bc0ee07248bda381a6b5043604f0 applied all three terminal outcomes once; the
ledger now records completed status and zero legacy ETH/SOL holds. The remaining
1 SOL hold belongs to a separate pending-approval request. Eight signed, stored
deposit receipts were classified as four confirming/completed testnet deposit
pairs. A malformed operational replay was isolated and removed by exact receipt
ID without processing; a file-backed valid replay then drained successfully.
Read-only audit task 32151eab01454bb6ac93718d696644bc exited zero with no
unprocessed receipts. PostgreSQL reports 40 Fireblocks receipts, zero unprocessed.

Migrations through 000072 were applied by task
615ae515844a41a4aec4afcee4fd7ca1. API revision 118 is stable 2/2 and withdrawal
revision 36 stable 1/1 on image digest
sha256:1a5a3a083018d1dae201775dc43bf9d602c8116111238b63063b3139a6f1bb09.
The deposit worker is stable 1/1. Public health returned 200. A zero-count handoff
was used for withdrawals; ECS briefly retained an old deployment task during its
own drain, but final inspection proved revision 36 is the sole running worker and
its log stream contains no errors. Test-money control, recipients and routes
remain unconfigured/default-off; no customer grant was issued by this release.

Current task: minimal staging-only issuance operations. The user explicitly chose
not to build a temporary HTTP/frontend administration surface. Provide a narrow
container command for read-only inspection, audited pilot preparation, proposal
and independent approval. It must use exact server-fetched reference prices,
internal_spot assets only, one active UTA, payload-bound idempotency and explicit
staging confirmation. It creates test_money_issuance journals only, never deposits
or withdrawal entitlement. Exact token allocations and grant execution remain a
separate explicit decision.

Activation increment in progress: pilot preparation explicitly enables only the
selected internal_spot ledger identities in deterministic symbol order and records
the complete selection in idempotency/audit evidence. Equal-symbol custody routes
and trading-pair halt status are not changed. The pilot policy represents the full
25-asset internal spot catalog including USDT; assets without trustworthy price
evidence remain ineligible for approval even when their ledger identity is enabled.

Staging activation evidence on 2026-09-09: commit bd21556c passed focused tests,
the full Go suite, go vet and PostgreSQL-backed activation tests, then was pushed
to origin/main. The staging-only executable was overlaid on the previously verified
backend image without changing API, worker or migration layers. ECR digest
sha256:2b89fea629199d8df6752c2ee3e85c93eae08382a5c497feda1342b8a79bb17d
is registered as API task definition 121 and passed a one-off inspection; the
long-running API service was not rolled forward because it does not expose this
temporary administration surface.

Policy 554e702f-7431-41e6-8ec7-7c2a7fbd4a8c explicitly enabled and represented
all 25 internal_spot ledger assets, including USDT, for the two named recipients.
It retained the aggregate 20,000 USDT ceiling. Toluk proposed every grant and
Favour approved every executed grant, so proposer and approver remained distinct.
For this staging pilot, Toluk remained the proposer and Favour remained the
distinct approver; Favour was also an approved grant recipient. Production
conflict-of-interest policy requires separate review.

Eighteen grants produced eighteen distinct balanced issuance journals and nine
available-balance credits per recipient. Exact approval-time values were
9,895.08280012 USDT for toluking001@gmail.com across AAVE, ARB, BNB, ETH, OP,
SHIB, TRX, USDC and WBTC; and 9,859.24574698 USDT for favourtolu57@gmail.com
across ADA, AVAX, BTC, LINK, POL, SOL, UNI, USDe and XRP. Two optional ceiling-
safe top-up requests were proposed but not approved and created no postings.
Read-only ledger audits 774c326dcd7e4a86b81ab722646d7750 and
1c21c41ad06c4c3faaa1760b1c821fda confirmed the credits and zero unprocessed
custody receipts. Grants remain test_money_issuance records, not deposits, and
create no withdrawal entitlement. Public verification returned healthy, all 24
pairs halted, zero nonzero last-trade prices and zero nonzero volumes. No market-
maker inventory or live quoting was released.

Completed increment: durable withdrawal submission boundary. Added immutable one-shot
dispatch evidence, revalidate controls/eligibility/net deposits and held funds,
and commit audit/outbox before a worker may call custody. Competing workers and
restarts must not resubmit an uncertain attempt. This increment does not implement
provider reconciliation or custody/fee reservations and cannot enable readiness.
Files: internal/datamanager/withdrawal_dispatch.go, scoped manager.go/worker.go,
worker tests, internal/testmoney/withdrawal_dispatch_test.go, migration
000070_withdrawal_dispatch up/down and this plan. Test concurrency, lost ACK,
failed commit, stale payload, revoked eligibility and deposit reorg before dispatch.

Completed task: withdrawal admission enforcement in the shared transaction layer.
Files: new internal/datamanager/withdrawal_entitlement.go and database tests;
new migration 000069_withdrawal_entitlement; scoped RequestWithdrawal and HTTP
error mapping changes; issuance readiness check. No enabling or grants yet.
Reuse credited deposit journals and existing withdrawal lifecycle as sources;
record immutable admission evidence. Worker revalidation/capacity and rollout
verification remain mandatory before readiness is enabled.

Completed task: isolated default-off customer issuance service, policy and
database tests. No HTTP wiring or activation until entitlement/isolation gates
pass. Outstanding audit, frontend, custody, lifecycle and release tasks remain open.

- [x] Inspect authoritative code/contracts and preserve worktree.
- [x] Verify AWS access, six service rollouts and historical audit report.
- [x] Record approved grant/withdrawal policy and this engineering plan.
- [ ] Audit current DB roles, controls, balances, deposits, withdrawals, route
  mappings and downstream command cessation using reviewed read-only queries.
- [x] Build default-off staging issuance: non-spendable counterpart accounts,
  balanced journals, versioned limits, recipient/reason, proposal/approval roles,
  payload-bound idempotency, sorted locks, atomic quota use and audit/outbox.
- [x] Implement isolated customer grant policy/service and immutable history,
  with real PostgreSQL transaction, concurrency, rollback and idempotency tests.
- [x] Complete the deliberately reduced issuance integration: trusted independent-
  reference adapter plus staging-only inspect/prepare/propose/approve command,
  reviewed policy/recipient preparation and runtime container wiring. No HTTP or
  frontend administration contract is in scope for this disposable pilot.
- [ ] Enforce test/mainnet isolation before enabling issuance: deployment/DB
  identity, custody workspace/source, chain verification and signing allowlist.
  Reject mismatched startup/configuration and prohibit production import.
- [ ] Establish deposit entitlement and withdrawal enforcement before issued
  funds can reach an existing withdrawal path, including bought-back-token tests.
- [x] Enforce the net-deposit ceiling at withdrawal admission with immutable
  evidence, user/asset serialization, lifecycle recovery and bought-back tests.
- [x] Finish pre-broadcast revalidation, durable dispatch claims, custody/fee
  reservation and explicit failed/unknown-outcome recovery before readiness.
- [x] Build durable one-shot dispatch boundary with fresh database eligibility,
  deposit-ceiling and held-journal checks; prove concurrency and lost-ACK retention.
- [x] Add maker-checker custody routes, fresh exact token/gas observations,
  fee caps, sorted capacity locks and atomic immutable dispatch reservations.
- [x] Reconcile capacity-backed dispatches by stable external ID; retain immutable
  not-found observations and close consumed/released capacity only on explicit
  provider terminal outcomes in the ledger transaction.
- [x] Audit and drain legacy in-flight dispatch before worker rollout; no overlapping
  old/new submitters. A committed intent alone is not proof of broadcast or
  non-submission.
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
handoff edits and package-lock.json. User subsequently authorized domain commits
and Git pushes. Staging deployment/activation still requires the safety gates.

## Minimal staging issuance command evidence (2026-09-06)

Created:

- C:/Users/toluk/Desktop/Limiance Backend/cmd/staging-test-money/main.go
- C:/Users/toluk/Desktop/Limiance Backend/cmd/staging-test-money/main_test.go
- C:/Users/toluk/Desktop/Limiance Backend/internal/testmoney/configure.go
- C:/Users/toluk/Desktop/Limiance Backend/internal/testmoney/configure_test.go
- C:/Users/toluk/Desktop/Limiance Backend/internal/testmoney/references.go
- C:/Users/toluk/Desktop/Limiance Backend/internal/testmoney/references_test.go

Modified:

- C:/Users/toluk/Desktop/Limiance Backend/Dockerfile
- C:/Users/toluk/Desktop/Limiance Backend/STAGING_TEST_MONEY_PLAN.md

No schema change. The command has no HTTP route. Mutating actions require both
APP_ENV=staging and --confirm-staging. Pilot preparation verifies distinct active
treasury operator/approver roles, explicit named recipients, and enabled
internal_spot assets; its normalized payload is idempotency-bound under a database
advisory lock. The global ceiling is exactly the 10,000-USDT atomic limit times
the number of named recipients. Configuration, audit and outbox commit together.

Proposal resolves exactly one active recipient UTA and exact internal_spot asset.
Approval uses two distinct successful server-configured venues selected from
Bybit, Binance, Coinbase, Kraken and Gate. Bid/ask strings are converted and
rounded conservatively with big.Int at USDT scale 8; float64 and requester-supplied
prices are never used. Existing service approval rechecks policy, roles, recipient,
asset, quota and distinct checker in the posting transaction. Grants remain
balanced test_money_issuance journals and create no deposit or entitlement event.

Focused command and package tests pass. Migration-backed PostgreSQL tests passed
in disposable container limiance-testmoney-pg-20260906, which was removed after
the run. New cases prove configuration idempotency, audit/outbox, exact ceiling,
role failure, payload conflict and production rejection. Reference tests prove
two distinct venues, deterministic duplicate handling, exact sub-atomic rounding,
crossed/missing quotes and custody-asset rejection. Full Go suite passed without
the integration variable; final vet/full-suite checks are recorded with the commit.

Remaining before grants: choose exact distinct token allocations totaling no more
than 10,000 USDT per tester, build and deploy the command image, inspect current
UTA/assets/control, prepare the explicit pilot, then perform separate propose and
approve invocations. Genuine order lifecycle and deposit-only withdrawal browser
evidence are still required before broader release; no grant implies readiness.

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

## Withdrawal admission evidence (2026-09-05)

AWS identity rechecked successfully: 041659147758. No login refresh was required.
No remote balances, controls, grants or deployments changed by this task.
Requested grants remain pending: 10,000 USDT equivalent of mixed tokens each for
toluking001@gmail.com and favourtolu57@gmail.com, with distinct approval.

Created under C:/Users/toluk/Desktop/Limiance Backend:

- internal/datamanager/withdrawal_entitlement.go
- internal/testmoney/withdrawal_entitlement_test.go
- migrations/000069_withdrawal_entitlement.up.sql
- migrations/000069_withdrawal_entitlement.down.sql

Modified under the same root:

- internal/datamanager/manager.go
- internal/platform/httpserver/withdrawals.go
- internal/testmoney/postgres.go
- internal/testmoney/postgres_test.go
- openapi/openapi.yaml
- STAGING_TEST_MONEY_PLAN.md

Admission reads credited, risk-approved, confirmed same-user/same-asset deposits
with a transaction hash and matching balanced posted deposit-credit journal.
It counts deposit identity once, regardless of duplicate journal references.
Existing completed/pending withdrawals consume the limit. Failed/cancelled/
rejected withdrawals need a compensating release journal to restore allowance;
absence of a provider ID alone is not proof of non-submission.
User/asset advisory locks serialize withdrawals across Funding and UTA accounts.
Admission evidence, hold, request, audit and outbox commit or roll back together.
Extra balances from issuance/trades do not increase the ceiling. Bought-back
same-token balances remain eligible within net deposits.

This increment uses existing deposit and withdrawal lifecycle records plus
immutable journal evidence; it does not introduce or claim a complete standalone
entitlement event stream/backfill. Current-source audit, reorg handling at dispatch
and reconciliation remain required before activation.

Protection applies while readiness/issuance is enabled or after any recorded
grant. A shared control lock and subsequent fresh history read prevent first-
issuance/disable races. Issuance itself now requires withdrawal_limits_ready;
the new column defaults false and is not changed on staging.

Lifecycle tests exposed two existing defects in the migrated schema/code:
untyped text/UUID parameters in cancellation/settlement/release journal inserts,
and a unique withdrawal_id constraint allowing only one journal per withdrawal.
Fixed casts and replaced uniqueness with (withdrawal_id,reference_type), retaining
append-only hold plus settlement/release. Down migration refuses destructive
rollback when admission/issuance or multi-journal withdrawal history exists.
Retry lookup now returns the original hold journal after terminal transitions;
idempotency is serialized and bound to user/account/asset/network/amount/address.
Changed-payload retries return a conflict rather than another request's result.

Validation: full go test ./... -count=1 passed with local PostgreSQL integration.
After final missing-ACK protection and indexes, reran all affected packages:
internal/testmoney, internal/withdrawals, internal/platform/httpserver, openapi;
all passed. Testmoney now has 21 top-level tests, including 9 new database tests
for withdrawal/readiness behavior. New cases cover issued-only balances, disabling
issuance, missing credit journals, reorged deposits, network mismatch, concurrent
accounts, cancellation, completion/failure/replay, missing ACK, bought-back funds,
changed-payload retries and outbox rollback. Execution balance fixtures are not
claimed to be actual matcher tests. Unconfigured external integrations may skip.

Test mapping: new deposit-entitlement/lifecycle acceptance items; supplements
Section 7 money-operation retry safety and Section 9 ledger evidence. Does not
satisfy actual custody withdrawal, live-order, browser or full Section 9 gates.
Next: durable pre-broadcast claims/revalidation and custody capacity/fees, then
issuance adapter/API integration and reviewed staging activation. Preserve all
other trading, frontend, audit, funding and release tasks above.

## Withdrawal dispatch boundary evidence (2026-09-05)

Created under C:/Users/toluk/Desktop/Limiance Backend:

- internal/datamanager/withdrawal_dispatch.go
- internal/testmoney/withdrawal_dispatch_test.go
- migrations/000070_withdrawal_dispatch.up.sql
- migrations/000070_withdrawal_dispatch.down.sql

Modified under the same root:

- internal/datamanager/manager.go
- internal/withdrawals/worker.go
- internal/withdrawals/worker_test.go
- STAGING_TEST_MONEY_PLAN.md

SQL adds withdrawal_dispatches keyed uniquely by withdrawal_id, with provider,
exact request JSON, enforcement mode and net-deposit/commitment evidence.
UPDATE/DELETE/TRUNCATE reject mutation; downgrade refuses recorded dispatches.
No ledger balances, entitlement, controls or external custody state are created
by this migration.

Core worker boundary:

```go
claimed, err := w.data.BeginWithdrawalDispatch(ctx, w.providerID, item)
if err != nil { return processed, err }
if !claimed { continue }
// Only this successful caller may now invoke CreateWithdrawal.
```

The database transaction rechecks the withdrawal control, current deposit ceiling
(including this withdrawal), active user/account/KYC including expiration, current
asset/vault route and exact candidate payload. A balanced posted original hold,
no terminal compensation and enough posted held balance are required. Shared
control and eligibility locks plus entitlement/withdrawal/shared-ledger locks
serialize the boundary. Intent, audit and outbox commit together before custody.
There is no lease or automatic rearm. Lost ACK, empty ACK, database ACK failure or
process death after commit retain the intent and hold. Candidate queries exclude
started attempts, and a stale competing candidate cannot claim again. The external
id remains the withdrawal UUID. Exact ACK replay is harmless; empty or conflicting
ACKs are rejected, including after terminal updates.

Six added PostgreSQL tests cover eight concurrent claimants yielding one winner,
restart exclusion/hold retention, immutable evidence and guarded rollback, reorg
and eligibility revocation, expiry, payload/route changes, missing posted hold,
outbox rollback and safe pre-commit retry, non-issuance compatibility and ACK replay.
Two added worker tests cover lost/empty ACK, database ACK failure, exact provider
payload and no custody call when the dispatch commit fails.

The original full-suite attempt found an assertion counting legacy request events
as custody ACKs. Request creation already uses withdrawal.submitted; the corrected
test distinguishes ACKs by provider_transaction_id without changing that contract.

This is only the durable submission-boundary increment, not the full withdrawal
safety/release gate. Next work must implement actual custody/fee capacity reads and
reservations, provider-identity-bound reconciliation, safe non-submission releases
and per-request rejection/queue behavior so invalid candidates cannot starve others.
Audit/drain legacy approved or in-flight attempts before replacing old workers:
the old binary does not observe dispatch intents. Database revalidation is not
proof of on-chain finality or sufficient custody; reorg and reconciliation remain
end-to-end gates. A stopped control prevents new claims, not an already committed
external call; command cessation must be observed separately.

Test mapping: approved deposit-only withdrawal and ambiguous-outcome acceptance
criteria; supplements Section 7 idempotency and Section 9 ledger/audit evidence.
No actual custody transaction, browser, live matcher or Section 9 sign-off claimed.
Readiness, issuance, treasury allocations and live quoting remain unactivated.
Both requested 10,000-USDT-equivalent mixed-token grants remain pending.

## Custody observation primitives evidence (2026-09-06)

Created:

- C:/Users/toluk/Desktop/Limiance Backend/internal/custody/fireblocks_observer_test.go

Modified:

- C:/Users/toluk/Desktop/Limiance Backend/internal/custody/provider.go
- C:/Users/toluk/Desktop/Limiance Backend/internal/custody/fireblocks.go
- C:/Users/toluk/Desktop/Limiance Backend/STAGING_TEST_MONEY_PLAN.md

No SQL/schema change. WithdrawalObserver is an optional read-only custody boundary
for vault/asset available balance, medium network-fee estimate and lookup by stable
external transaction ID. The Fireblocks implementation uses the documented
GET /v1/vault/accounts/{vaultAccountId}/{assetId}, POST
/v1/transactions/estimate_fee, and GET
/v1/transactions/external_tx_id/{externalTxId} paths with the existing signed JWT.
It neither exposes credentials nor changes provider or database state.

ProviderAmountToAtomic accepts only canonical unsigned base-10 fixed-point input,
scales it exactly with big.Int, and rejects signs, whitespace, exponent notation,
excess precision, decimals outside 0..36 and values beyond NUMERIC(78,0). No
float64 is used. Missing available balance, missing medium networkFee, malformed
responses and external-ID mismatches fail closed. HTTP 404 from lookup produces a
not-observed result only; it does not prove non-submission, release a hold or
authorize retry.

Tests validate signed method/path/body contracts, exact available-balance and
medium-fee extraction, exact atomic conversion including 78 digits, lookup
identity/status/hash, 404 semantics and incomplete-response rejection. This
satisfies only the provider-observation portion of custody capacity and ambiguous
outcome test items. It does not reserve capacity, select/verify the fee asset,
bind observation freshness, reconcile a dispatch, or broadcast a real testnet
withdrawal. Readiness, issuance, both tester grants and quoting remain off.

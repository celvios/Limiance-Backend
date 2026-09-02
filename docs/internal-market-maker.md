# Internal Market Maker Operations

The internal market maker runs inside the Go monolith and submits every order through `trading.Service`. It cannot write directly to the matching engine, bypass ledger holds, or bypass the order gateway's idempotency and fee resolution.

## Safe defaults

Migration `000065_internal_market_maker.up.sql` creates a configuration row for every current spot pair. All pair rows are disabled. The global control row starts with:

- `enabled = false`
- `dry_run = true`
- `kill_switch = true`
- no user or account identity

With these defaults the five-second worker performs no feed requests and sends no orders. A configured identity must be a dedicated active user with one active UTA account; capital must be journaled into that account through an approved treasury workflow.

## Reference price and risk

Each enabled pair obtains Bybit, Coinbase, Kraken, Binance, and Gate public spot bid/ask data concurrently. At least two fresh, valid, independently named feeds are required. The worker converts decimals to the pair's integer price scale, computes each midpoint, and then takes the median without floating-point arithmetic. It halts a pair when feeds are stale or their maximum divergence exceeds `max_divergence_bps`.

Before quoting, the worker enforces the configured spread, quantity, price tick, maximum base inventory, maximum quote notional per side, and maximum daily loss. It requires enough base inventory to support the sell quote and enough remaining inventory capacity to support the buy quote. The ordinary gateway independently enforces available balances and pair limits.

Live cycles cancel existing pending, open, and partially filled quotes through the ordinary cancellation path before placing post-only GTC bid and ask orders. Command keys are deterministic per pair, side, and five-second window and are claimed in PostgreSQL, so restarts and duplicate cycles cannot emit the same command twice.

## Activation checklist

Activation is an operations change and must not be inferred from a deployment. Direct updates to the market-maker tables are not an activation mechanism; use the authenticated `/v2/admin/market-maker` workflow so the maker, checker, evidence, inventory journals, audit event, and outbox event remain atomic.

1. Verify the operator, approver, and dedicated internal user are active; grant the two human users their distinct treasury roles through the audited role-management endpoint.
2. Select funded system treasury accounts as the sources for both base and quote inventory. Approval creates the balanced transfer journals; never insert balances directly.
3. Define conservative atomic limits for each desired pair; the workflow keeps every unlisted pair disabled.
4. A `treasury_operator` proposes `configure_dry_run` with the dedicated user's email, inventory source accounts and atomic amounts, and complete per-pair atomic policies.
5. A distinct `treasury_approver` approves it. The transaction verifies active identities and roles, posts balanced inventory journals, disables unlisted pairs, and starts the approved pairs with `enabled=true`, `dry_run=true`, and `kill_switch=false`.
6. Observe dry-run decisions long enough to verify at least two independent fresh venues, the median/divergence guard, and emergency stop. No dry-run command reaches the order gateway.
7. Reconcile every inventory journal and run post-only place/cancel/fill/reject lifecycle tests without fabricating trades for market-data display.
8. A `treasury_operator` proposes `release_live` with all four evidence gates affirmed. A distinct `treasury_approver` approves it; only that approval may set `dry_run=false`.

Emergency stop is one transaction:

```sql
UPDATE market_maker_control
SET kill_switch = TRUE, updated_at = now()
WHERE singleton = TRUE;
```

The emergency-stop endpoint sets the switch immediately. On its next cycle the worker cancels existing pending, open, and partially filled quotes through the normal order gateway before returning the kill-switch halt.

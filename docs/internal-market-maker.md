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

Each enabled pair obtains Bybit, Coinbase, and Kraken public spot bid/ask data concurrently. At least two fresh, valid feeds are required. The worker converts decimals to the pair's integer price scale, computes each midpoint, and then takes the median without floating-point arithmetic. It halts a pair when feeds are stale or their maximum divergence exceeds `max_divergence_bps`.

Before quoting, the worker enforces the configured spread, quantity, price tick, maximum base inventory, maximum quote notional per side, and maximum daily loss. It requires enough base inventory to support the sell quote and enough remaining inventory capacity to support the buy quote. The ordinary gateway independently enforces available balances and pair limits.

Live cycles cancel existing pending, open, and partially filled quotes through the ordinary cancellation path before placing post-only GTC bid and ask orders. Command keys are deterministic per pair, side, and five-second window and are claimed in PostgreSQL, so restarts and duplicate cycles cannot emit the same command twice.

## Activation checklist

Activation is an operations change and must not be inferred from a deployment.

1. Create and approve the dedicated internal user and UTA account.
2. Fund both base and quote assets with balanced treasury journals.
3. Set conservative limits for each desired pair; keep every other pair disabled.
4. Set `user_id` and `account_id` in `market_maker_control` while leaving the global kill switch on.
5. Enable only the approved pair rows.
6. Set global `enabled=true` but retain `dry_run=true`; observe decisions and feed health.
7. Obtain treasury/risk approval for the dry-run evidence.
8. Set `dry_run=false`, then release `kill_switch=false` as the final action.

Emergency stop is one transaction:

```sql
UPDATE market_maker_control
SET kill_switch = TRUE, updated_at = now()
WHERE singleton = TRUE;
```

The switch prevents new cycles. Existing orders must also be canceled through the normal order cancellation/admin emergency-stop workflow; the later operations task owns that control-plane endpoint.

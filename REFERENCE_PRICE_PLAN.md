# Reference price display and verification

- [x] Inspect exchange contract and frontend market views.
- [x] Add separate reference fields while preserving trade-derived statistics.
- [x] Gate reference output on current controls, successful decisions, and age.
- [x] Test database queries and stale/stopped behavior in an isolated database.
- [x] Integrate market and spot displays with atomic string formatting.
- [ ] Verify changing staging samples and ledger/order safety evidence.
- [ ] Commit tested domain changes and deploy authorized staging changes.
- [ ] Confirm the intended task 11 before beginning it (user confirmed it is not audit A11).

Validation: full Go suite passed with PostgreSQL integration enabled against
disposable local reference_tests database. Frontend formatter tests, Vite build,
and SSR component checks passed for changing/stopped/expired prices. Existing
frontend lint warnings remain outside the market views. Browser interaction and
staging deployment have not yet been verified. Spot market panels now read API
data; actual order placement and custody-backed liquidity remain outstanding.

The earlier absence of CloudWatch warnings did not prove price movement or
per-pair success. Acceptance requires timestamped price samples. Browser approval
proved the stop request succeeded; downstream command cessation still needs
direct verification. No inventory funding or live release is authorized by this plan.

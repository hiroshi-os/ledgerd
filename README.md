# ledgerd

Idempotent payments API on a double-entry ledger (Go + Postgres).

Showcase project for payments-grade API correctness: Stripe-style idempotency, payment-intent state machines, and money-movement invariants. Built for Billy to polish before resume use.

## 60-second quickstart

```bash
docker compose up -d
export DATABASE_URL=postgres://ledgerd:ledgerd@localhost:5432/ledgerd?sslmode=disable
export LEDGERD_BOOTSTRAP_API_KEY=sk_test_ledgerd
go run ./cmd/ledgerd
```

In another shell:

```bash
# Create a merchant account
curl -sS http://127.0.0.1:8080/v1/accounts \
  -H "Authorization: Bearer sk_test_ledgerd" \
  -H "Idempotency-Key: acct-1" \
  -H "Content-Type: application/json" \
  -d '{"currency":"usd"}'

# Create, confirm, and capture a payment intent (replace ACCOUNT_ID)
curl -sS http://127.0.0.1:8080/v1/payment_intents \
  -H "Authorization: Bearer sk_test_ledgerd" \
  -H "Idempotency-Key: pi-1" \
  -H "Content-Type: application/json" \
  -d '{"account_id":"ACCOUNT_ID","amount":2500,"currency":"usd","processor_script":["succeed"]}'

curl -sS -X POST http://127.0.0.1:8080/v1/payment_intents/PI_ID/confirm \
  -H "Authorization: Bearer sk_test_ledgerd" \
  -H "Idempotency-Key: confirm-1" \
  -H "Content-Type: application/json" \
  -d '{}'

curl -sS -X POST http://127.0.0.1:8080/v1/payment_intents/PI_ID/capture \
  -H "Authorization: Bearer sk_test_ledgerd" \
  -H "Idempotency-Key: capture-1" \
  -H "Content-Type: application/json" \
  -d '{}'
```

Amounts are integer **minor units** (cents). Floats are rejected.

## Payment intent states

```mermaid
stateDiagram-v2
  [*] --> requires_confirmation
  requires_confirmation --> processing: confirm (succeed / drop)
  requires_confirmation --> failed: confirm (decline)
  processing --> succeeded: capture
  succeeded --> partially_refunded: partial refund
  succeeded --> refunded: full refund
  partially_refunded --> partially_refunded: partial refund
  partially_refunded --> refunded: remaining refund
```

## Consistency model

- Double-entry: every capture/refund writes one ledger transaction whose entries sum to **zero** per currency.
- Ledger tables are **append-only** (DB triggers reject UPDATE/DELETE).
- Merchant balances are materialized and verified against `SUM(ledger_entries)`.
- Balance checks use blocking `SELECT ... FOR UPDATE` (sorted lock order). See [DESIGN.md](DESIGN.md).
- Idempotency-Key on all POSTs: first response stored; same key + different params → 422; concurrent in-flight → 409; TTL default 24h; validation failures are not stored; keys scoped per API key; record written in the **same DB transaction** as the effect.

## Tests

```bash
export DATABASE_URL=postgres://ledgerd:ledgerd@localhost:5432/ledgerd_test?sslmode=disable
go test -race -count=1 ./...
```

CI runs the same against a Postgres 16 service container, plus `gofmt` and `go vet`.

## Measured results

All figures below are copied from a real run recorded in [`bench/RESULTS.md`](bench/RESULTS.md) (hardware, date, commit SHA, exact command). Do not invent numbers.

| Metric | Value | Source |
| --- | --- | --- |
| Property sequences | **200** | `TestPropertyPaymentSequences` |
| Fault runs passed | **1000/1000** | `TestFaultInjectionAfterCommitBeforeResponse` |
| Confirm+capture throughput | **190.56 /s** | `cmd/loadgen -c 32 -n 500` |
| Latency p50 / p95 / p99 | **163.28 / 181.12 / 206.27 ms** | same loadgen run |
| Idempotent-replay latency p50 / p95 / p99 | **0.54 / 0.58 / 0.59 ms** | loadgen `-replay 200` |

Hardware for that run: GitHub Actions `ubuntu-latest`, AMD EPYC 9V74 (4 cores allocated), 16373452 kB RAM, date `2026-09-26T14:14:26Z`, checkout SHA `050a8a184ec1dee50b61ba7cb8e5fe9c0640893e` (details in RESULTS.md).

```bash
go run ./cmd/loadgen -base http://127.0.0.1:8080 -token sk_test_ledgerd -c 32 -n 500 -replay 200
```

## Honesty / limitations

What this is **not**:

- Not a real card processor, not PCI-DSS scoped, not production payment rails.
- Not multi-region, not serializable isolation by default (uses read committed + row locks).
- Not a webhook delivery system: `events` is an outbox table only; nothing publishes it.
- Append-only is enforced by triggers, not by connecting as a non-owner role.
- Simulated processor timeouts/drops are deterministic scripts/RNG, not network chaos.
- Throughput numbers are single-host, single-Postgres; they are not capacity guarantees.
- Advisory-lock collision on idempotency keys is theoretically possible (64-bit hash); uniqueness after commit is still enforced by the unique constraint.

## DRAFT resume bullets

> Marked **DRAFT** for Billy to edit before any resume use.

- **DRAFT:** Built a Stripe-style idempotent payments API in Go with Postgres double-entry ledgering, property tests, and fault-injection proving exactly-once capture under retry.
- **DRAFT:** Enforced money-movement invariants (balanced entries, non-negative merchant balances, refund ≤ capture) in both application code and database constraints/triggers.

## License

MIT (if/when Billy adds a LICENSE).

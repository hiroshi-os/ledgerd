# Benchmark and correctness measurement log

Every number cited in the README must appear here with hardware, date (UTC), commit SHA, and the exact command. No estimates.

## Pending first measured run

Local Postgres binaries could not be installed on the development machine that authored this PR (installer downloads timed out; many hosts DNS-resolved to `192.0.2.1`). Figures will be filled from a completed `go test` + `cmd/loadgen` run against Postgres 16 (Docker Compose or CI `measure` workflow) and committed before merge.

| Field | Value |
| --- | --- |
| status | pending |
| note | Do not copy numbers into the README until a completed run is pasted below. |

<!-- Template for the next section:

## Run YYYY-MM-DD

- hardware: <CPU model>, <N> cores / <M> logical, <RAM>
- date_utc: ...
- commit: <sha>
- commands:
  - `go test -race -count=1 ./...`
  - `./loadgen -base http://127.0.0.1:8080 -token sk_test_ledgerd -c 32 -n 500 -replay 200`

### Correctness
- property sequences passed: N
- fault runs passed: N/N

### Loadgen (confirm+capture)
- ok / err / elapsed / throughput_confirm_capture_per_s
- confirm_capture_latency_ms p50 / p95 / p99
- idempotent_replay_latency_ms p50 / p95 / p99

-->

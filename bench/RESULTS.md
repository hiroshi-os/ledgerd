# Benchmark and correctness measurement log

Every number cited in the README must appear here with hardware, date (UTC), commit SHA, and the exact command. No estimates.

## Run 2026-09-26 (GitHub Actions `measure` workflow)

- hardware: GitHub Actions `ubuntu-latest` (x86_64), CPU model `AMD EPYC 9V74 80-Core Processor`, **4** cores allocated to the job, **16373452** kB RAM (~15.6 GiB)
- date_utc: `2026-09-26T14:14:26Z`
- commit (Actions checkout `git rev-parse HEAD` on the PR merge ref): `050a8a184ec1dee50b61ba7cb8e5fe9c0640893e`
- workflow head SHA (branch tip for this run): `bcf91b01f2f5c86bb60c3008c50cf702add0efbb`
- workflow run: https://github.com/hiroshi-os/ledgerd/actions/runs/36247802207
- artifact: `measure-out` (id `10907987226`)

### Correctness

commands:

```bash
go test -race -count=1 ./...
```

- property sequences passed: **200** (`TestPropertyPaymentSequences`, seed=99)
- fault runs passed: **1000/1000** (`TestFaultInjectionAfterCommitBeforeResponse`)

### Loadgen (confirm+capture)

commands:

```bash
go build -o ledgerd ./cmd/ledgerd
go build -o loadgen ./cmd/loadgen
./ledgerd &
./loadgen -base http://127.0.0.1:8080 -token sk_test_ledgerd -c 32 -n 500 -replay 200
```

- ok=**500** err=**0** elapsed=**2.623852856s** throughput_confirm_capture_per_s=**190.56** samples=**500**
- confirm_capture_latency_ms p50=**163.28** p95=**181.12** p99=**206.27**
- idempotent_replay_latency_ms p50=**0.54** p95=**0.58** p99=**0.59** samples=**200**

### Notes

- An earlier measure run on the same day (`36247093067`) recorded loadgen `ok=0 err=500` because concurrent confirms deadlocked the shared pgx pool while calling the simulated processor. That was fixed with a dedicated `ProcessorPool`; this section is the post-fix run only.

## Run 2026-09-26 (Ruby client CI)

- hardware: GitHub Actions Linux x86_64, OS **Ubuntu 24.04.5 LTS**, CPU **Intel(R) Xeon(R) Platinum 8370C CPU @ 2.80GHz**, **4** cores, **16372440** kB RAM
- ruby: **ruby 3.2.11 (2026-03-27 revision 5483bfc1ae) [x86_64-linux]**
- date_utc: `2026-09-26T14:44:38Z`
- commit (Actions checkout `git rev-parse HEAD`): `06bd5ecfad2f8617fe1c9f17e41d085abb69609a`
- workflow head SHA (branch tip): `c37cb437bebd263cc57ee8c5303a6255ad9dad86`
- workflow run: https://github.com/hiroshi-os/ledgerd/actions/runs/36249531920
- artifact: `ruby-test-out` (id `10907664579`)

command:

```bash
cd clients/ruby && ruby -Ilib:test test/client_test.rb
```

- **10** runs, **88** assertions, **0** failures, **0** errors, **0** skips
- Finished in **0.025611s**

### Retry matrix (covered by those tests)

| Condition | Retried? | Covered |
| --- | --- | --- |
| Network error | yes | (unit via connection error path; not separately counted above) |
| 409 / 429 / 5xx | yes, up to max_retries | yes |
| 400 / 401 / 404 / 422 | no (exactly one request) | yes |
| Idempotency-Key reused on retries | yes | yes |
| Retry-After honoured | yes | yes |

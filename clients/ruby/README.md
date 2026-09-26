# Ruby client for ledgerd

Stdlib-only (`Net::HTTP`) client with Stripe-style **Idempotency-Key** reuse on retries. Written from scratch for this repo — not an official SDK and **not published to RubyGems**.

## Install from path

```bash
# from the ledgerd repo root
cd clients/ruby
ruby -Ilib -r ledgerd -e 'puts Ledgerd::VERSION'
```

Or in another project:

```ruby
# Gemfile
gem "ledgerd", path: "../ledgerd/clients/ruby"
```

Ruby **3.2+**. No runtime gem dependencies.

## Usage

```ruby
require "ledgerd"

client = Ledgerd::Client.new(
  api_key: "sk_test_ledgerd",
  base_url: "http://127.0.0.1:8080",
  max_retries: 2
)

account = client.accounts.create(currency: "usd")
pi = client.payment_intents.create(
  account_id: account["id"],
  amount: 2500,
  currency: "usd",
  processor_script: ["succeed"]
)
client.payment_intents.confirm(pi["id"])
client.payment_intents.capture(pi["id"])
client.refunds.create(payment_intent_id: pi["id"], amount: 500)
```

Pass your own key with `idempotency_key:`; otherwise the client generates one UUID per logical call and reuses it on every retry of that call.

## Retry semantics

| Condition | Retried? |
| --- | --- |
| Network error (refused, reset, timeout, …) | yes, up to `max_retries` |
| HTTP **409** (idempotency key in flight) | yes |
| HTTP **429** (honours `Retry-After` when present) | yes |
| HTTP **5xx** | yes |
| HTTP **400 / 401 / 404 / 422** (and other non-listed 4xx) | **no** |

Backoff: exponential with full jitter, base **0.5s**, cap **8s**, then stop after `max_retries`.

Typed errors: `APIConnectionError`, `InvalidRequestError`, `AuthenticationError`, `IdempotencyError`, `RateLimitError`, `APIError` (plus `NotFoundError`). Each exposes `http_status`, `body`, and `request_id` when available.

## Tests

```bash
cd clients/ruby
ruby -Ilib:test test/client_test.rb
```

Optional live server:

```bash
LEDGERD_INTEGRATION=1 LEDGERD_API_KEY=sk_test_ledgerd \
  ruby -Ilib:test test/integration_test.rb
```

## Honesty / limitations

- Not published to RubyGems; path/git install only.
- Not an official Stripe or ledgerd “SDK product” — a showcase client.
- Retries are best-effort; they do not make the server correct by themselves.
- Does not implement the full ledgerd surface (ledger transaction list/get omitted).
- No connection pooling beyond `Net::HTTP` per request.
- WEBrick-backed unit tests are not a substitute for production traffic.

## DRAFT resume bullets

- **DRAFT:** Built a stdlib Ruby client for an idempotent payments API that reuses one Idempotency-Key across retries and never retries terminal 4xx responses.

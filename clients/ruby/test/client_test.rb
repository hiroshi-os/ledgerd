# frozen_string_literal: true

require "minitest/autorun"
require "json"
require "socket"
require "stringio"
require "securerandom"

$LOAD_PATH.unshift File.expand_path("../lib", __dir__)
require "ledgerd"

# Minimal HTTP/1.1 server using only stdlib Socket (no webrick gem).
module FakeServerHelper
  FakeRequest = Struct.new(:method, :path, :headers, :body, keyword_init: true)

  def with_fake_server(handler)
    queue = Queue.new
    server = TCPServer.new("127.0.0.1", 0)
    port = server.addr[1]
    stop = false

    thread = Thread.new do
      until stop
        begin
          client = server.accept
        rescue IOError, Errno::EBADF
          break
        end
        Thread.new(client) do |conn|
          begin
            req = read_http_request(conn)
            queue << req
            status, out_headers, out_body = handler.call(req, queue.size)
            write_http_response(conn, status, out_headers, out_body)
          rescue StandardError
            # ignore client disconnects
          ensure
            conn.close
          end
        end
      end
    end

    base = "http://127.0.0.1:#{port}"
    yield base, queue
  ensure
    stop = true
    begin
      server&.close
    rescue StandardError
      nil
    end
    thread&.join(2)
  end

  def read_http_request(conn)
    request_line = conn.gets
    raise IOError, "empty request" if request_line.nil?

    method, path, = request_line.split(" ", 3)
    headers = {}
    while (line = conn.gets)
      break if line == "\r\n" || line == "\n"

      name, value = line.split(":", 2)
      headers[name.strip.downcase] = value.to_s.strip
    end
    length = headers["content-length"].to_i
    body = length.positive? ? conn.read(length) : nil
    FakeRequest.new(method: method, path: path, headers: headers, body: body)
  end

  def write_http_response(conn, status, out_headers, out_body)
    body = out_body.to_s
    headers = { "Content-Type" => "application/json", "Content-Length" => body.bytesize.to_s }
    out_headers.each { |k, v| headers[k] = v.to_s }
    conn.write "HTTP/1.1 #{status} X\r\n"
    headers.each { |k, v| conn.write "#{k}: #{v}\r\n" }
    conn.write "\r\n"
    conn.write body
  end
end

class ClientRetryTest < Minitest::Test
  include FakeServerHelper

  def build_client(base_url, max_retries: 2, sleeper: nil, random: nil)
    Ledgerd::Client.new(
      api_key: "sk_test",
      base_url: base_url,
      max_retries: max_retries,
      open_timeout: 2,
      read_timeout: 2,
      sleeper: sleeper || ->(_) {},
      random: random || Random.new(1)
    )
  end

  def test_retries_reuse_identical_idempotency_key
    keys = []
    handler = lambda do |req, n|
      keys << req.headers["idempotency-key"]
      if n < 3
        [503, {}, JSON.generate({ error: { message: "boom", code: "internal" } })]
      else
        [201, {}, JSON.generate({ id: "acct_1", object: "account" })]
      end
    end

    with_fake_server(handler) do |base, _|
      client = build_client(base, max_retries: 2)
      result = client.accounts.create(currency: "usd", idempotency_key: "fixed-key-1")
      assert_equal "acct_1", result["id"]
    end

    assert_equal 3, keys.size
    assert_equal ["fixed-key-1"], keys.uniq
  end

  def test_auto_generated_key_stable_across_retries
    keys = []
    handler = lambda do |req, n|
      keys << req.headers["idempotency-key"]
      if n < 2
        [500, {}, JSON.generate({ error: { message: "up", code: "internal" } })]
      else
        [201, {}, JSON.generate({ id: "acct_2" })]
      end
    end

    with_fake_server(handler) do |base, _|
      client = build_client(base, max_retries: 2)
      client.accounts.create(currency: "usd")
    end

    assert_equal 2, keys.size
    refute_nil keys.first
    assert_equal keys.first, keys.last
    assert_match(/\A[0-9a-f-]{36}\z/i, keys.first)
  end

  def test_non_retryable_4xx_exactly_one_request
    [400, 401, 404, 422].each do |status|
      count = 0
      handler = lambda do |_req, _n|
        count += 1
        body = case status
               when 401
                 { error: { message: "nope", code: "unauthorized", type: "authentication_error" } }
               when 404
                 { error: { message: "missing", code: "not_found" } }
               when 422
                 { error: { message: "bad", code: "parameter_invalid" } }
               else
                 { error: { message: "bad", code: "parameter_invalid" } }
               end
        [status, {}, JSON.generate(body)]
      end

      with_fake_server(handler) do |base, _|
        client = build_client(base, max_retries: 3)
        err_class = case status
                    when 401 then Ledgerd::AuthenticationError
                    when 404 then Ledgerd::NotFoundError
                    else Ledgerd::InvalidRequestError
                    end
        assert_raises(err_class) { client.accounts.create(currency: "usd") }
      end
      assert_equal 1, count, "status #{status} should not retry"
    end
  end

  def test_422_idempotency_mismatch_is_idempotency_error_not_retried
    count = 0
    handler = lambda do |_req, _n|
      count += 1
      [422, {}, JSON.generate({ error: { message: "mismatch", code: "idempotency_key_mismatch", type: "idempotency_error" } })]
    end

    with_fake_server(handler) do |base, _|
      client = build_client(base, max_retries: 3)
      err = assert_raises(Ledgerd::IdempotencyError) { client.accounts.create(currency: "usd") }
      assert_equal 422, err.http_status
    end
    assert_equal 1, count
  end

  def test_409_429_5xx_retried_up_to_max_retries
    {
      409 => Ledgerd::IdempotencyError,
      429 => Ledgerd::RateLimitError,
      503 => Ledgerd::APIError
    }.each do |status, err_class|
      count = 0
      handler = lambda do |_req, _n|
        count += 1
        [status, { "Retry-After" => "0" }, JSON.generate({ error: { message: "retry", code: "x" } })]
      end

      with_fake_server(handler) do |base, _|
        client = build_client(base, max_retries: 2)
        assert_raises(err_class) { client.accounts.create(currency: "usd") }
      end
      assert_equal 3, count, "status #{status}"
    end
  end

  def test_backoff_delays_within_bounds
    sleeps = []
    sleeper = ->(s) { sleeps << s }
    random = Object.new
    def random.rand
      1.0
    end

    handler = lambda do |_req, _n|
      [503, {}, JSON.generate({ error: { message: "x" } })]
    end

    with_fake_server(handler) do |base, _|
      client = Ledgerd::Client.new(
        api_key: "sk_test",
        base_url: base,
        max_retries: 3,
        sleeper: sleeper,
        random: random
      )
      assert_raises(Ledgerd::APIError) { client.accounts.create(currency: "usd") }
    end

    assert_equal [0.5, 1.0, 2.0], sleeps
    sleeps.each { |d| assert d >= 0 && d <= 8.0 }
  end

  def test_retry_after_honoured
    sleeps = []
    sleeper = ->(s) { sleeps << s }
    handler = lambda do |_req, n|
      if n == 1
        [429, { "Retry-After" => "1.5" }, JSON.generate({ error: { message: "slow" } })]
      else
        [201, {}, JSON.generate({ id: "ok" })]
      end
    end

    with_fake_server(handler) do |base, _|
      client = build_client(base, max_retries: 2, sleeper: sleeper)
      client.accounts.create(currency: "usd")
    end

    assert_equal [1.5], sleeps
  end

  def test_typed_errors_expose_status_and_body
    handler = lambda do |_req, _n|
      [401, { "X-Request-Id" => "req_abc" }, JSON.generate({ error: { message: "bad key", code: "unauthorized" } })]
    end

    with_fake_server(handler) do |base, _|
      client = build_client(base)
      err = assert_raises(Ledgerd::AuthenticationError) { client.accounts.retrieve("x") }
      assert_equal 401, err.http_status
      assert_includes err.body, "bad key"
      assert_equal "req_abc", err.request_id
    end
  end

  def test_payment_intent_and_refund_paths
    seen = []
    handler = lambda do |req, _n|
      seen << [req.method, req.path]
      [200, {}, JSON.generate({ id: "ok", status: "succeeded" })]
    end

    with_fake_server(handler) do |base, _|
      client = build_client(base)
      client.payment_intents.create(account_id: "a", amount: 100, currency: "usd")
      client.payment_intents.confirm("pi_1")
      client.payment_intents.capture("pi_1")
      client.payment_intents.retrieve("pi_1")
      client.refunds.create(payment_intent_id: "pi_1", amount: 50)
      client.refunds.retrieve("re_1")
    end

    assert_includes seen, ["POST", "/v1/payment_intents"]
    assert_includes seen, ["POST", "/v1/payment_intents/pi_1/confirm"]
    assert_includes seen, ["POST", "/v1/payment_intents/pi_1/capture"]
    assert_includes seen, ["GET", "/v1/payment_intents/pi_1"]
    assert_includes seen, ["POST", "/v1/refunds"]
    assert_includes seen, ["GET", "/v1/refunds/re_1"]
  end
end

class UtilTest < Minitest::Test
  def test_backoff_never_exceeds_max
    rng = Random.new(42)
    20.times do |attempt|
      d = Ledgerd::Util.backoff_delay(attempt, base: 0.5, max: 8.0, random: rng)
      assert d >= 0
      assert d <= 8.0
    end
  end
end

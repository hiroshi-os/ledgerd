# frozen_string_literal: true

# Optional integration test against a live ledgerd server.
# Enable with: LEDGERD_INTEGRATION=1 LEDGERD_BASE_URL=... LEDGERD_API_KEY=... ruby test/integration_test.rb

require "minitest/autorun"

$LOAD_PATH.unshift File.expand_path("../lib", __dir__)
require "ledgerd"

class IntegrationTest < Minitest::Test
  def setup
    skip "set LEDGERD_INTEGRATION=1" unless ENV["LEDGERD_INTEGRATION"] == "1"
    @client = Ledgerd::Client.new(
      api_key: ENV.fetch("LEDGERD_API_KEY", "sk_test_ledgerd"),
      base_url: ENV.fetch("LEDGERD_BASE_URL", "http://127.0.0.1:8080")
    )
  end

  def test_create_confirm_capture
    acct = @client.accounts.create(currency: "usd")
    pi = @client.payment_intents.create(
      account_id: acct["id"],
      amount: 2500,
      currency: "usd",
      processor_script: ["succeed"]
    )
    confirmed = @client.payment_intents.confirm(pi["id"])
    assert_equal "processing", confirmed["status"]
    captured = @client.payment_intents.capture(pi["id"])
    assert_equal "succeeded", captured["status"]
  end
end

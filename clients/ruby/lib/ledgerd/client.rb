# frozen_string_literal: true

module Ledgerd
  class Client
    attr_reader :accounts, :payment_intents, :refunds, :http

    def initialize(api_key:, base_url: "http://127.0.0.1:8080", max_retries: 2,
                   open_timeout: 5, read_timeout: 30, sleeper: nil, random: nil)
      raise ArgumentError, "api_key is required" if api_key.nil? || api_key.empty?

      @http = HTTP.new(
        api_key: api_key,
        base_url: base_url,
        max_retries: max_retries,
        open_timeout: open_timeout,
        read_timeout: read_timeout,
        sleeper: sleeper,
        random: random
      )
      @accounts = Resources::Accounts.new(@http)
      @payment_intents = Resources::PaymentIntents.new(@http)
      @refunds = Resources::Refunds.new(@http)
    end
  end
end

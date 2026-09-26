# frozen_string_literal: true

module Ledgerd
  module Resources
    class Base
      def initialize(http)
        @http = http
      end

      private

      def get(path)
        @http.request(:get, path).json
      end

      def post(path, params: {}, idempotency_key: nil)
        @http.request(:post, path, params: params, idempotency_key: idempotency_key).json
      end
    end

    class Accounts < Base
      def create(currency:, idempotency_key: nil)
        post("/v1/accounts", params: { currency: currency }, idempotency_key: idempotency_key)
      end

      def retrieve(id)
        get("/v1/accounts/#{id}")
      end
    end

    class PaymentIntents < Base
      def create(account_id:, amount:, currency:, processor_script: [], idempotency_key: nil)
        post(
          "/v1/payment_intents",
          params: {
            account_id: account_id,
            amount: amount,
            currency: currency,
            processor_script: processor_script
          },
          idempotency_key: idempotency_key
        )
      end

      def retrieve(id)
        get("/v1/payment_intents/#{id}")
      end

      def confirm(id, idempotency_key: nil)
        post("/v1/payment_intents/#{id}/confirm", params: {}, idempotency_key: idempotency_key)
      end

      def capture(id, idempotency_key: nil)
        post("/v1/payment_intents/#{id}/capture", params: {}, idempotency_key: idempotency_key)
      end
    end

    class Refunds < Base
      def create(payment_intent_id:, amount:, idempotency_key: nil)
        post(
          "/v1/refunds",
          params: { payment_intent_id: payment_intent_id, amount: amount },
          idempotency_key: idempotency_key
        )
      end

      def retrieve(id)
        get("/v1/refunds/#{id}")
      end
    end
  end
end

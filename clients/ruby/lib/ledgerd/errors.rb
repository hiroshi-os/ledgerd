# frozen_string_literal: true

module Ledgerd
  class Error < StandardError
    attr_reader :http_status, :body, :request_id

    def initialize(message = nil, http_status: nil, body: nil, request_id: nil)
      @http_status = http_status
      @body = body
      @request_id = request_id
      super(message || default_message)
    end

    def default_message
      self.class.name
    end
  end

  class APIConnectionError < Error; end

  class APIError < Error; end

  class InvalidRequestError < Error; end

  class AuthenticationError < Error; end

  class IdempotencyError < Error; end

  class RateLimitError < Error; end

  class NotFoundError < InvalidRequestError; end
end

# frozen_string_literal: true

require "json"
require "net/http"
require "uri"
require "time"

module Ledgerd
  # Low-level HTTP with retries. One Idempotency-Key per logical request,
  # reused on every retry of that request.
  class HTTP
    RETRYABLE_STATUSES = [409, 429].freeze

    Response = Struct.new(:status, :headers, :body, :json, keyword_init: true)

    def initialize(api_key:, base_url:, max_retries:, open_timeout:, read_timeout:,
                   sleeper: nil, random: nil)
      @api_key = api_key
      @base_url = base_url.chomp("/")
      @max_retries = max_retries
      @open_timeout = open_timeout
      @read_timeout = read_timeout
      @sleeper = sleeper || ->(seconds) { sleep(seconds) }
      @random = random || Random.new
      @base_delay = 0.5
      @max_delay = 8.0
    end

    def request(method, path, params: nil, idempotency_key: nil)
      key = idempotency_key
      key = Util.generate_idempotency_key if key.nil? && method != :get

      body = params.nil? ? nil : JSON.generate(params)
      attempt = 0

      loop do
        begin
          response = execute_once(method, path, body: body, idempotency_key: key)
        rescue APIConnectionError
          raise if attempt >= @max_retries

          delay = Util.backoff_delay(attempt, base: @base_delay, max: @max_delay, random: @random)
          @sleeper.call(delay)
          attempt += 1
          next
        end

        return response if success?(response.status)

        error = map_error(response)
        raise error unless retryable?(response.status, error) && attempt < @max_retries

        delay = retry_delay(attempt, response)
        @sleeper.call(delay)
        attempt += 1
      end
    end

    private

    def success?(status)
      status >= 200 && status < 300
    end

    def retryable?(status, error)
      return true if status >= 500
      return true if RETRYABLE_STATUSES.include?(status)
      false
    end

    def retry_delay(attempt, response)
      if response.status == 429
        ra = Util.parse_retry_after(response.headers["retry-after"] || response.headers["Retry-After"])
        return ra if ra && ra.positive?
      end
      Util.backoff_delay(attempt, base: @base_delay, max: @max_delay, random: @random)
    end

    def execute_once(method, path, body:, idempotency_key:)
      uri = URI.join("#{@base_url}/", path.sub(%r{\A/}, ""))
      http = Net::HTTP.new(uri.host, uri.port)
      http.use_ssl = uri.scheme == "https"
      http.open_timeout = @open_timeout
      http.read_timeout = @read_timeout

      req = build_request(method, uri, body)
      req["Authorization"] = "Bearer #{@api_key}"
      req["Accept"] = "application/json"
      req["User-Agent"] = "ledgerd-ruby/#{VERSION}"
      req["Idempotency-Key"] = idempotency_key if idempotency_key

      begin
        res = http.request(req)
      rescue Timeout::Error, Errno::ECONNREFUSED, Errno::ECONNRESET, Errno::EHOSTUNREACH,
             Errno::ENETUNREACH, SocketError, EOFError, IOError => e
        raise APIConnectionError, "network error: #{e.class}: #{e.message}"
      end

      headers = {}
      res.each_header { |k, v| headers[k.downcase] = v }
      parsed = nil
      if res.body && !res.body.empty?
        begin
          parsed = JSON.parse(res.body)
        rescue JSON::ParserError
          parsed = nil
        end
      end

      Response.new(status: res.code.to_i, headers: headers, body: res.body, json: parsed)
    end

    def build_request(method, uri, body)
      case method
      when :get
        Net::HTTP::Get.new(uri)
      when :post
        r = Net::HTTP::Post.new(uri)
        r["Content-Type"] = "application/json"
        r.body = body || "{}"
        r
      else
        raise ArgumentError, "unsupported method #{method}"
      end
    end

    def map_error(response)
      request_id = response.headers["request-id"] || response.headers["x-request-id"]
      message = error_message(response)
      kwargs = { http_status: response.status, body: response.body, request_id: request_id }

      case response.status
      when 400, 422
        # 422 idempotency key mismatch is still an IdempotencyError
        code = error_code(response)
        if response.status == 422 && code.to_s.include?("idempotency")
          IdempotencyError.new(message, **kwargs)
        else
          InvalidRequestError.new(message, **kwargs)
        end
      when 401
        AuthenticationError.new(message, **kwargs)
      when 404
        NotFoundError.new(message, **kwargs)
      when 409
        IdempotencyError.new(message, **kwargs)
      when 429
        RateLimitError.new(message, **kwargs)
      when 500..599
        APIError.new(message, **kwargs)
      else
        APIError.new(message, **kwargs)
      end
    end

    def error_message(response)
      if response.json.is_a?(Hash)
        err = response.json["error"]
        if err.is_a?(Hash)
          return err["message"] || err["code"] || response.body
        end
      end
      response.body.to_s
    end

    def error_code(response)
      return nil unless response.json.is_a?(Hash)

      err = response.json["error"]
      err.is_a?(Hash) ? err["code"] : nil
    end
  end
end

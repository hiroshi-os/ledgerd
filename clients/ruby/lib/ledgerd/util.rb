# frozen_string_literal: true

require "securerandom"
require "time"

module Ledgerd
  module Util
    module_function

    def generate_idempotency_key
      SecureRandom.uuid
    end

    # Exponential backoff with full jitter, bounded by base and max.
    # attempt is 0-based (0 = first retry after the initial failure).
    def backoff_delay(attempt, base:, max:, random: Random.new)
      ceiling = [max, base * (2**attempt)].min
      random.rand * ceiling
    end

    def parse_retry_after(header)
      return nil if header.nil? || header.to_s.strip.empty?

      value = header.to_s.strip
      if value.match?(/\A\d+(\.\d+)?\z/)
        value.to_f
      else
        Time.httpdate(value) - Time.now
      end
    rescue ArgumentError
      nil
    end
  end
end

# frozen_string_literal: true

Gem::Specification.new do |spec|
  spec.name          = "ledgerd"
  spec.version       = "0.1.0"
  spec.authors       = ["Billy"]
  spec.email         = ["billy@example.com"]

  spec.summary       = "Ruby client for the ledgerd payments API"
  spec.description   = "Stdlib Net::HTTP client with Stripe-style Idempotency-Key reuse on retries."
  spec.homepage      = "https://github.com/hiroshi-os/ledgerd"
  spec.license       = "MIT"
  spec.required_ruby_version = ">= 3.2.0"

  spec.files = Dir.chdir(__dir__) do
    Dir["lib/**/*", "README.md", "ledgerd.gemspec"]
  end
  spec.require_paths = ["lib"]

  # No runtime gem dependencies — Net::HTTP / JSON / SecureRandom only.
end

-- ledgerd schema. Amounts are bigint minor units.
-- Ledger tables are append-only via triggers. The demo connects as the table
-- owner, so GRANT/REVOKE is not a second line of defense (owners bypass grants).
-- Triggers still fire for the owner. See DESIGN.md.

CREATE TABLE api_keys (
    id uuid PRIMARY KEY,
    token_sha256 bytea NOT NULL UNIQUE,
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE accounts (
    id uuid PRIMARY KEY,
    api_key_id uuid REFERENCES api_keys (id),
    kind text NOT NULL CHECK (kind IN ('merchant', 'clearing')),
    currency char(3) NOT NULL CHECK (currency ~ '^[a-z]{3}$'),
    available_balance_minor bigint NOT NULL DEFAULT 0,
    balance_version bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT merchant_nonnegative CHECK (kind <> 'merchant' OR available_balance_minor >= 0),
    CONSTRAINT clearing_has_no_api_key CHECK (
        (kind = 'clearing' AND api_key_id IS NULL)
        OR (kind = 'merchant' AND api_key_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX accounts_one_clearing_per_currency
    ON accounts (currency)
    WHERE kind = 'clearing';

INSERT INTO accounts (id, api_key_id, kind, currency, available_balance_minor, balance_version, created_at)
VALUES
    ('00000000-0000-4000-8000-000000000001', NULL, 'clearing', 'usd', 0, 0, now()),
    ('00000000-0000-4000-8000-000000000002', NULL, 'clearing', 'eur', 0, 0, now()),
    ('00000000-0000-4000-8000-000000000003', NULL, 'clearing', 'gbp', 0, 0, now());

CREATE TABLE payment_intents (
    id uuid PRIMARY KEY,
    api_key_id uuid NOT NULL REFERENCES api_keys (id),
    account_id uuid NOT NULL REFERENCES accounts (id),
    amount_minor bigint NOT NULL CHECK (amount_minor > 0),
    currency char(3) NOT NULL,
    status text NOT NULL CHECK (status IN (
        'requires_confirmation',
        'processing',
        'succeeded',
        'failed',
        'refunded',
        'partially_refunded'
    )),
    amount_captured_minor bigint NOT NULL DEFAULT 0 CHECK (amount_captured_minor >= 0),
    amount_refunded_minor bigint NOT NULL DEFAULT 0 CHECK (amount_refunded_minor >= 0),
    processor_script jsonb NOT NULL DEFAULT '[]'::jsonb,
    processor_ref text,
    failure_reason text,
    version bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT refund_not_above_capture CHECK (amount_refunded_minor <= amount_captured_minor),
    CONSTRAINT captured_matches_status CHECK (
        (
            status IN ('succeeded', 'refunded', 'partially_refunded')
            AND amount_captured_minor = amount_minor
        )
        OR (
            status NOT IN ('succeeded', 'refunded', 'partially_refunded')
            AND amount_captured_minor = 0
        )
    ),
    CONSTRAINT refund_status_matches CHECK (
        (
            status = 'refunded'
            AND amount_refunded_minor = amount_captured_minor
            AND amount_captured_minor > 0
        )
        OR (
            status = 'partially_refunded'
            AND amount_refunded_minor > 0
            AND amount_refunded_minor < amount_captured_minor
        )
        OR (
            status NOT IN ('refunded', 'partially_refunded')
            AND amount_refunded_minor = 0
        )
    )
);

CREATE INDEX payment_intents_account ON payment_intents (account_id);
CREATE INDEX payment_intents_api_key ON payment_intents (api_key_id);

CREATE TABLE ledger_transactions (
    id uuid PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('capture', 'refund')),
    currency char(3) NOT NULL,
    payment_intent_id uuid REFERENCES payment_intents (id),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX one_capture_per_intent
    ON ledger_transactions (payment_intent_id)
    WHERE kind = 'capture';

CREATE INDEX ledger_txn_pi ON ledger_transactions (payment_intent_id);

CREATE TABLE refunds (
    id uuid PRIMARY KEY,
    api_key_id uuid NOT NULL REFERENCES api_keys (id),
    payment_intent_id uuid NOT NULL REFERENCES payment_intents (id),
    amount_minor bigint NOT NULL CHECK (amount_minor > 0),
    currency char(3) NOT NULL,
    status text NOT NULL CHECK (status = 'succeeded'),
    ledger_transaction_id uuid NOT NULL REFERENCES ledger_transactions (id),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX refunds_pi ON refunds (payment_intent_id);

CREATE TABLE ledger_entries (
    id uuid PRIMARY KEY,
    transaction_id uuid NOT NULL REFERENCES ledger_transactions (id),
    account_id uuid NOT NULL REFERENCES accounts (id),
    amount_minor bigint NOT NULL,
    currency char(3) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX ledger_entries_txn ON ledger_entries (transaction_id);
CREATE INDEX ledger_entries_account ON ledger_entries (account_id);

CREATE TABLE idempotency_keys (
    id uuid PRIMARY KEY,
    api_key_id uuid NOT NULL REFERENCES api_keys (id),
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 255),
    method text NOT NULL,
    path text NOT NULL,
    body_sha256 bytea NOT NULL,
    response_status integer NOT NULL CHECK (response_status BETWEEN 100 AND 599),
    response_body bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (api_key_id, idempotency_key)
);

CREATE INDEX idempotency_keys_created ON idempotency_keys (created_at);

CREATE TABLE processor_attempts (
    payment_intent_id uuid PRIMARY KEY,
    attempt_count integer NOT NULL CHECK (attempt_count >= 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE processor_charges (
    payment_intent_id uuid PRIMARY KEY,
    outcome text NOT NULL CHECK (outcome IN ('succeed', 'decline', 'drop')),
    processor_ref text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE events (
    id uuid PRIMARY KEY,
    type text NOT NULL,
    aggregate_id uuid NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);

CREATE INDEX events_unpublished ON events (created_at) WHERE published_at IS NULL;
CREATE INDEX events_aggregate ON events (aggregate_id, created_at);

CREATE OR REPLACE FUNCTION ledgerd_reject_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'append-only: % on % is forbidden', TG_OP, TG_TABLE_NAME
        USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER ledger_entries_append_only
    BEFORE UPDATE OR DELETE ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledgerd_reject_mutation();

CREATE TRIGGER ledger_transactions_append_only
    BEFORE UPDATE OR DELETE ON ledger_transactions
    FOR EACH ROW EXECUTE FUNCTION ledgerd_reject_mutation();

CREATE TRIGGER events_append_only
    BEFORE UPDATE OR DELETE ON events
    FOR EACH ROW EXECUTE FUNCTION ledgerd_reject_mutation();

CREATE OR REPLACE FUNCTION ledgerd_entries_balanced() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    total bigint;
    txn_currency char(3);
BEGIN
    SELECT COALESCE(SUM(amount_minor), 0)
      INTO total
      FROM ledger_entries
     WHERE transaction_id = NEW.transaction_id;

    IF total <> 0 THEN
        RAISE EXCEPTION 'unbalanced ledger transaction %: sum=%', NEW.transaction_id, total
            USING ERRCODE = '23514';
    END IF;

    SELECT currency INTO txn_currency FROM ledger_transactions WHERE id = NEW.transaction_id;

    IF EXISTS (
        SELECT 1
          FROM ledger_entries e
         WHERE e.transaction_id = NEW.transaction_id
           AND e.currency <> txn_currency
    ) THEN
        RAISE EXCEPTION 'ledger transaction % mixes currencies', NEW.transaction_id
            USING ERRCODE = '23514';
    END IF;

    IF EXISTS (
        SELECT 1
          FROM ledger_entries e
          JOIN accounts a ON a.id = e.account_id
         WHERE e.transaction_id = NEW.transaction_id
           AND e.currency <> a.currency
    ) THEN
        RAISE EXCEPTION 'ledger entry currency does not match account for %', NEW.transaction_id
            USING ERRCODE = '23514';
    END IF;

    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER ledger_entries_balanced
    AFTER INSERT ON ledger_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledgerd_entries_balanced();

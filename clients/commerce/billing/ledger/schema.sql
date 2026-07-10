-- Double-Entry Ledger Schema
-- All monetary amounts are stored in the currency's smallest unit (cents for USD).
-- Every ledger_entry MUST have postings that sum to exactly zero.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Chart of Accounts
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS ledger_accounts (
    id              TEXT        PRIMARY KEY,
    tenant_id       TEXT        NOT NULL,
    name            TEXT        NOT NULL,
    type            TEXT        NOT NULL CHECK (type IN ('asset', 'liability', 'equity', 'revenue', 'expense')),
    currency        TEXT        NOT NULL DEFAULT 'usd',
    normal_balance  TEXT        NOT NULL CHECK (normal_balance IN ('debit', 'credit')),
    metadata        JSONB       NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, name)
);

COMMENT ON TABLE  ledger_accounts IS 'Chart of accounts. Each account belongs to exactly one tenant.';
COMMENT ON COLUMN ledger_accounts.type IS 'asset | liability | equity | revenue | expense';
COMMENT ON COLUMN ledger_accounts.normal_balance IS 'debit for asset/expense, credit for liability/equity/revenue';

-- System account naming conventions:
--   platform:cash                    -- platform settlement account (asset)
--   platform:fees                    -- collected platform fees (revenue)
--   platform:reserves                -- held reserves (liability)
--   platform:disputes_held           -- funds frozen for disputes (asset)
--   customer_balance:{customer_id}   -- prepaid customer credit (liability)
--   merchant_settlement:{merchant_id} -- owed to merchant (liability)

CREATE INDEX idx_ledger_accounts_tenant ON ledger_accounts (tenant_id);

-- ---------------------------------------------------------------------------
-- 2. Journal Entries (groups of postings)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS ledger_entries (
    id                  TEXT        PRIMARY KEY,
    tenant_id           TEXT        NOT NULL,
    idempotency_key     TEXT        NOT NULL,
    description         TEXT        NOT NULL DEFAULT '',
    payment_intent_id   TEXT,
    refund_id           TEXT,
    payout_id           TEXT,
    transfer_id         TEXT,
    dispute_id          TEXT,
    metadata            JSONB       NOT NULL DEFAULT '{}',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, idempotency_key)
);

COMMENT ON TABLE ledger_entries IS 'Journal entries. Each entry groups one or more postings that must sum to zero.';

CREATE INDEX idx_ledger_entries_tenant          ON ledger_entries (tenant_id);
CREATE INDEX idx_ledger_entries_payment_intent  ON ledger_entries (payment_intent_id) WHERE payment_intent_id IS NOT NULL;
CREATE INDEX idx_ledger_entries_refund          ON ledger_entries (refund_id)          WHERE refund_id IS NOT NULL;
CREATE INDEX idx_ledger_entries_payout          ON ledger_entries (payout_id)          WHERE payout_id IS NOT NULL;
CREATE INDEX idx_ledger_entries_dispute         ON ledger_entries (dispute_id)         WHERE dispute_id IS NOT NULL;
CREATE INDEX idx_ledger_entries_created         ON ledger_entries (tenant_id, created_at);

-- ---------------------------------------------------------------------------
-- 3. Individual Postings (debits and credits)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS ledger_postings (
    id          TEXT        PRIMARY KEY,
    entry_id    TEXT        NOT NULL REFERENCES ledger_entries(id),
    account_id  TEXT        NOT NULL REFERENCES ledger_accounts(id),
    amount      BIGINT      NOT NULL,  -- positive = debit, negative = credit
    currency    TEXT        NOT NULL DEFAULT 'usd',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE  ledger_postings IS 'Individual debit/credit legs of a journal entry.';
COMMENT ON COLUMN ledger_postings.amount IS 'Positive = debit, negative = credit. Sum per entry MUST equal zero.';

CREATE INDEX idx_ledger_postings_entry   ON ledger_postings (entry_id);
CREATE INDEX idx_ledger_postings_account ON ledger_postings (account_id);

-- ---------------------------------------------------------------------------
-- 4. Authorization Holds
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS ledger_holds (
    id                  TEXT        PRIMARY KEY,
    tenant_id           TEXT        NOT NULL,
    account_id          TEXT        NOT NULL REFERENCES ledger_accounts(id),
    amount              BIGINT      NOT NULL CHECK (amount > 0),
    currency            TEXT        NOT NULL DEFAULT 'usd',
    status              TEXT        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'captured', 'voided', 'expired')),
    payment_intent_id   TEXT,
    captured_entry_id   TEXT        REFERENCES ledger_entries(id),
    expires_at          TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE ledger_holds IS 'Pending authorization holds that reduce available balance without moving funds.';

CREATE INDEX idx_ledger_holds_tenant  ON ledger_holds (tenant_id);
CREATE INDEX idx_ledger_holds_account ON ledger_holds (account_id, status);
CREATE INDEX idx_ledger_holds_payment ON ledger_holds (payment_intent_id) WHERE payment_intent_id IS NOT NULL;
CREATE INDEX idx_ledger_holds_expires ON ledger_holds (expires_at) WHERE status = 'pending';

-- ---------------------------------------------------------------------------
-- 5. Materialized Balances
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS ledger_balances (
    account_id          TEXT        NOT NULL REFERENCES ledger_accounts(id),
    currency            TEXT        NOT NULL DEFAULT 'usd',
    posted_balance      BIGINT      NOT NULL DEFAULT 0,  -- sum of all postings
    pending_balance     BIGINT      NOT NULL DEFAULT 0,  -- sum of pending postings (future use)
    held_balance        BIGINT      NOT NULL DEFAULT 0,  -- sum of active holds
    available_balance   BIGINT      NOT NULL DEFAULT 0,  -- posted - held (computed)
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (account_id, currency)
);

COMMENT ON TABLE  ledger_balances IS 'Materialized balance cache. Updated by application logic on every posting/hold change.';
COMMENT ON COLUMN ledger_balances.available_balance IS 'posted_balance - held_balance. Always recomputed, never set directly.';

-- ---------------------------------------------------------------------------
-- 6. Constraints: enforce zero-sum postings per entry
-- ---------------------------------------------------------------------------

-- This function is called by a trigger after inserts on ledger_postings.
-- It verifies that all postings for a given entry sum to zero.
-- Note: this only fires reliably if all postings for an entry are inserted
-- in a single transaction before commit.

CREATE OR REPLACE FUNCTION check_entry_balance() RETURNS TRIGGER AS $$
DECLARE
    posting_sum BIGINT;
BEGIN
    SELECT COALESCE(SUM(amount), 0) INTO posting_sum
    FROM ledger_postings
    WHERE entry_id = NEW.entry_id;

    IF posting_sum <> 0 THEN
        RAISE EXCEPTION 'ledger entry % postings sum to % (must be 0)', NEW.entry_id, posting_sum;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- The trigger fires as a constraint trigger at commit time so all postings
-- for a single entry can be inserted before the check runs.

CREATE CONSTRAINT TRIGGER trg_check_entry_balance
    AFTER INSERT ON ledger_postings
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW
    EXECUTE FUNCTION check_entry_balance();

COMMIT;

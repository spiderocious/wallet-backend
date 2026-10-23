-- +goose Up

-- The ledger is append-only. Every balance change is two rows with the same
-- reference, one debit and one credit, inserted in one transaction.
CREATE TABLE ledger_entries (
  id           TEXT PRIMARY KEY,                     -- le_ULID
  wallet_id    TEXT NOT NULL REFERENCES wallets(id),
  type         TEXT NOT NULL CHECK (type IN ('credit', 'debit')),
  amount_kobo  BIGINT NOT NULL CHECK (amount_kobo > 0),
  reference    TEXT NOT NULL,                        -- tx_… or signup:<user_id>
  kind         TEXT NOT NULL CHECK (kind IN ('transfer', 'signup_bonus', 'treasury_seed')),
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ledger_wallet_created ON ledger_entries (wallet_id, created_at DESC, id DESC);
CREATE INDEX idx_ledger_reference ON ledger_entries (reference);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_ledger_immutability() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'ledger_entries is append-only: % not allowed', TG_OP;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER ledger_no_mutate BEFORE UPDATE OR DELETE ON ledger_entries
  FOR EACH ROW EXECUTE FUNCTION enforce_ledger_immutability();

-- Read cache of SUM(ledger_entries), maintained only by the trigger below.
-- Application code never writes this table.
CREATE TABLE wallet_balances (
  wallet_id     TEXT PRIMARY KEY REFERENCES wallets(id),
  balance_kobo  BIGINT NOT NULL DEFAULT 0,
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The overdraw rule. Postgres cannot defer a CHECK constraint, so it is a
-- deferrable constraint trigger: it runs at COMMIT, after both rows of a
-- post are in, so the debit and the credit can be inserted in either order.
-- It raises SQLSTATE 23514 (check_violation) with the constraint name so the
-- application maps it exactly as it would a CHECK. The mint wallet is the
-- one exemption: it is the counterparty to creating money.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_balance_non_negative() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
  wallet_kind TEXT;
BEGIN
  IF NEW.balance_kobo < 0 THEN
    SELECT kind INTO wallet_kind FROM wallets WHERE id = NEW.wallet_id;
    IF wallet_kind IS DISTINCT FROM 'mint' THEN
      RAISE EXCEPTION 'balance of wallet % would be % kobo', NEW.wallet_id, NEW.balance_kobo
        USING ERRCODE = 'check_violation', CONSTRAINT = 'balance_non_negative', TABLE = 'wallet_balances';
    END IF;
  END IF;
  RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER balance_non_negative
  AFTER INSERT OR UPDATE OF balance_kobo ON wallet_balances
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION enforce_balance_non_negative();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION update_wallet_balance() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO wallet_balances (wallet_id, balance_kobo, updated_at)
  VALUES (
    NEW.wallet_id,
    CASE WHEN NEW.type = 'credit' THEN NEW.amount_kobo ELSE -NEW.amount_kobo END,
    now()
  )
  ON CONFLICT (wallet_id) DO UPDATE
    SET balance_kobo = wallet_balances.balance_kobo +
      CASE WHEN NEW.type = 'credit' THEN NEW.amount_kobo ELSE -NEW.amount_kobo END,
        updated_at = now();
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER after_ledger_insert AFTER INSERT ON ledger_entries
  FOR EACH ROW EXECUTE FUNCTION update_wallet_balance();

-- +goose Down
DROP TABLE wallet_balances;
DROP TABLE ledger_entries;
DROP FUNCTION update_wallet_balance;
DROP FUNCTION enforce_balance_non_negative;
DROP FUNCTION enforce_ledger_immutability;

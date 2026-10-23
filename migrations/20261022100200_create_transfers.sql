-- +goose Up
CREATE TABLE transfers (
  id                 TEXT PRIMARY KEY,                 -- tx_ULID
  from_wallet_id     TEXT NOT NULL REFERENCES wallets(id),
  to_wallet_id       TEXT NOT NULL REFERENCES wallets(id),
  amount_kobo        BIGINT NOT NULL CHECK (amount_kobo > 0),
  status             TEXT NOT NULL CHECK (status IN ('posted', 'rejected')),
  reject_reason      TEXT,
  idempotency_key    TEXT,
  client_created_at  TIMESTAMPTZ,                      -- device clock, when the user tapped send
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_transfers_from ON transfers (from_wallet_id, created_at DESC);
CREATE INDEX idx_transfers_to ON transfers (to_wallet_id, created_at DESC);

-- +goose Down
DROP TABLE transfers;

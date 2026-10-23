-- +goose Up
CREATE TABLE users (
  id          TEXT PRIMARY KEY,          -- u_ULID
  phone       TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL,
  pin_hash    TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION touch_updated_at() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER users_updated_at BEFORE UPDATE ON users
  FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

CREATE TABLE wallets (
  id          TEXT PRIMARY KEY,          -- w_ULID
  user_id     TEXT REFERENCES users(id), -- NULL for treasury and mint
  -- mint: the single counterparty to creating money. It is the only wallet
  -- the overdraw rule exempts, so even seeding is a balanced post.
  kind        TEXT NOT NULL DEFAULT 'user' CHECK (kind IN ('user', 'treasury', 'mint')),
  currency    TEXT NOT NULL DEFAULT 'NGN',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_wallets_user ON wallets (user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX idx_wallets_treasury ON wallets (kind) WHERE kind = 'treasury';

-- +goose Down
DROP TABLE wallets;
DROP TABLE users;
DROP FUNCTION touch_updated_at;

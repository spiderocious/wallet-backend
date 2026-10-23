-- +goose Up
CREATE TABLE idempotency_keys (
  id               TEXT PRIMARY KEY,
  key              TEXT NOT NULL,
  user_id          TEXT NOT NULL,
  endpoint         TEXT NOT NULL,
  request_hash     TEXT NOT NULL,
  response_status  INTEGER NOT NULL,
  response_body    TEXT NOT NULL,            -- stored verbatim so a replay is byte-identical
  expires_at       TIMESTAMPTZ NOT NULL,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT idempotency_keys_key_user_key UNIQUE (key, user_id)
);

CREATE INDEX idx_idempotency_keys_expires ON idempotency_keys (expires_at);

-- +goose Down
DROP TABLE idempotency_keys;

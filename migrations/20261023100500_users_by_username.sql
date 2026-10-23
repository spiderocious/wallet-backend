-- +goose Up
-- People find each other by a chosen username, not a phone number. Existing
-- rows keep their value as the username; the unique index comes along.
ALTER TABLE users RENAME COLUMN phone TO username;
ALTER TABLE users RENAME CONSTRAINT users_phone_key TO users_username_key;
ALTER TABLE users ADD CONSTRAINT users_username_format
  CHECK (username ~ '^[a-z0-9_]{3,24}$');

-- +goose Down
ALTER TABLE users DROP CONSTRAINT users_username_format;
ALTER TABLE users RENAME CONSTRAINT users_username_key TO users_phone_key;
ALTER TABLE users RENAME COLUMN username TO phone;

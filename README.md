# wallet-backend

Go + Gin API for the Wallet demo from the DevFest Akure 2026 workshop *Designing Offline Enabled Data Intensive Web Apps: Web Workers, Cache Strategy, Storage Tiers, Sync, Queues, and Runtime Observability*.

Frontend: [wallet-frontend](https://github.com/spiderocious/wallet-frontend). Sibling demo: [fieldkit-backend](https://github.com/spiderocious/fieldkit-backend).

Live: `https://wallet-api-production-cec7.up.railway.app` (health at `/health`).

## The one rule

The device holds intent; the server holds the balance. Money moves only through `ledger.Post`, which writes two balanced rows in one transaction. A trigger maintains the balance cache, and a deferrable constraint trigger refuses an overdraw at COMMIT (SQLSTATE 23514, constraint `balance_non_negative`). The rejected transfer is recorded with its reason and the refusal returns the real balance.

| Route | Purpose |
|---|---|
| `POST /api/v1/auth/signup`, `/login` | sign-up credits ₦5,000 from the treasury; both return token, user and wallet |
| `GET /api/v1/wallet` | the confirmed balance |
| `POST /api/v1/transfers` | send by phone with an `Idempotency-Key`; 422 `insufficient_balance`, 404 `recipient_not_found`, 422 `self_transfer` |
| `GET /api/v1/transactions` | cursor-paged posted transfers, sent and received |
| `GET /api/v1/events` | server-sent events for the signed-in user (`transfer.received`) |
| `GET /api/v1/ops/stage` | the projector view: balance, received today, distinct keys, latest rows |
| `GET /api/v1/ops/reconcile` | compares the balance cache to `SUM(ledger_entries)`; reports, never corrects |
| `GET /api/v1/users?phone=` | recipient lookup, id and name only |
| `POST /api/v1/telemetry` | batches of device events |

## Run

```sh
cp .env.example .env            # edit DATABASE_URL and JWT_SECRET
go run ./cmd/migrate            # creates the database if missing, applies migrations
go run ./cmd/seed               # treasury + presenter 08030000000 / 1234; idempotent
go run ./cmd/api                # :8081 by default
```

Verify: `go test ./... -count=1` (contract tests on an embedded Postgres), `golangci-lint run ./...`.

## Deploy

`Dockerfile` builds `api`, `migrate` and `seed`; `entrypoint.sh` runs them in that order. Set `DATABASE_URL`, `JWT_SECRET` (32+ chars) and `CORS_ORIGIN`. The SSE route needs a server `WriteTimeout` of 0 and a proxy that does not buffer (`Cache-Control: no-transform` is sent).

## Layout and rules

See [docs/conventions.md](docs/conventions.md).

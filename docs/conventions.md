# Backend conventions

Both Go backends follow these. They are the Node service rules carried over to Go + Gin.

**Shape.** `cmd/` has one main per binary. `internal/features/<name>/` owns a feature end to end: types, repo, service, handler, `Register`. Cross-cutting code lives in `internal/{httpx,middleware,db,ids,env,logx,pagination}`.

**Responses.** Only `httpx` calls `c.JSON`. Success is `{ data, meta? }`; failure is `{ error: { reason, message, severity, field_errors?, data?, request_id } }`. Handlers call `httpx.OK/Created/Accepted/Fail`.

**Errors.** Services return `*httpx.DomainError` values built with `httpx.Domain/NotFound/Validation`; no result wrappers. `reason` is a stable machine code clients branch on; `message` comes from the catalog in `httpx/messages.go`. Diagnostics go to the log, never to the client.

**Services never see `*gin.Context`.** They take `context.Context` and typed inputs. Caller identity comes from `middleware.AgentIDFrom(ctx)` (or `UserIDFrom`).

**Idempotency.** Every write accepts `Idempotency-Key`. The middleware captures the key and a body hash and hands the body back untouched. The service runs the write through `middleware.Execute`, which stores `(key, caller, endpoint, hash, status, body)` in the same transaction. Replays return the stored body byte for byte; a mismatched body is 422 `idempotency_mismatch`.

**Versioned updates.** `UPDATE … WHERE id = $1 AND version = $2`. Zero rows and a live record means 409 `version_conflict` with the current record in `error.data`.

**Pagination.** Cursor only: `?limit&cursor` in, `meta: { next_cursor, has_more }` out. Cursors are opaque base64url of `(last_sort_key, last_id)`.

**Money** (wallet). `money.Kobo` is int64. The ledger is append-only (a trigger refuses UPDATE/DELETE), balances are a trigger-maintained cache, and the overdraw rule is a deferrable constraint trigger evaluated at COMMIT that raises SQLSTATE 23514 with constraint name `balance_non_negative`.

**Migrations.** goose as a library, SQL files embedded, forward-only. plpgsql bodies sit between `-- +goose StatementBegin/End`.

**Tests.** Contract tests in `internal/app` hit the real engine over `httptest` against an embedded Postgres (no Docker). Every reason code the frontend branches on has a test that produces it.

**Logs.** slog JSON with `request_id`; `pin`, `pin_hash`, `phone`, `authorization` and `token` are redacted at the handler level.

#!/bin/sh
# Release steps, then the server. Migrate creates the database if it is
# missing and applies pending migrations; seed is idempotent.
set -e
/app/migrate
/app/seed
exec /app/api

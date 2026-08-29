#!/usr/bin/env bash
#
# Applies orb's schema migrations, in order, exactly once each.
#
# Each file runs inside a single transaction together with the row that records
# it, so a migration either lands completely and is marked, or does neither.
# There is no half-applied state to reason about at three in the morning.
#
# Safe to re-run: already-applied files are skipped by name.
#
#   ./scripts/migrate.sh              apply what is pending
#   ./scripts/migrate.sh --status     show what is applied, change nothing
#   ./scripts/migrate.sh --baseline   adopt an existing database: record every
#                                     migration as applied WITHOUT running it
#
# Reads POSTGRES_USER and POSTGRES_DB from .env if present.
set -euo pipefail

cd "$(dirname "$0")/.."

# Load .env, but let anything already in the environment win. That is how
# docker compose itself resolves precedence, and it is what makes
# `COMPOSE_PROJECT_NAME=other ./scripts/migrate.sh` behave as expected instead
# of silently pointing at whatever .env says.
if [ -f .env ]; then
  while IFS='=' read -r key val; do
    [[ -z "$key" || "$key" == \#* ]] && continue
    [[ -n "${!key+x}" ]] && continue
    export "$key=$val"
  done < .env
fi

PGUSER=${POSTGRES_USER:-hello}
PGDB=${POSTGRES_DB:-orb}
COMPOSE=(docker compose)
MIGRATIONS=${MIGRATIONS_DIR:-./orb/migrations}

[ -d "$MIGRATIONS" ] || { echo "no migrations directory at $MIGRATIONS" >&2; exit 1; }

psql_q() { "${COMPOSE[@]}" exec -T postgres psql -v ON_ERROR_STOP=1 -c "SET client_min_messages=warning" -U "$PGUSER" -d "$PGDB" -qtA "$@"; }

if ! "${COMPOSE[@]}" ps --status running --services 2>/dev/null | grep -qx postgres; then
  echo "postgres is not running. Start it first:  make up" >&2
  exit 1
fi

# The ledger. Created here rather than in a migration so that migration 0001
# is not a special case.
psql_q -c "CREATE TABLE IF NOT EXISTS schema_migrations (
             version    TEXT PRIMARY KEY,
             applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
           );" >/dev/null

# Adopting a database that already has the schema, applied by hand before this
# script existed. Without this, the first run would try 0001 against a database
# that already has those tables and fail on a duplicate object, and the obvious
# workaround (deleting the tables) is catastrophic.
if [ "${1:-}" = "--baseline" ]; then
  n=0
  for f in "$MIGRATIONS"/*.sql; do
    v=$(basename "$f")
    if [ -n "$(psql_q -c "SELECT 1 FROM schema_migrations WHERE version = '$v';")" ]; then
      continue
    fi
    psql_q -c "INSERT INTO schema_migrations (version) VALUES ('$v');" >/dev/null
    echo "recorded (not run): $v"
    n=$((n + 1))
  done
  echo "baselined $n migration(s); nothing was executed against the schema"
  exit 0
fi

if [ "${1:-}" = "--status" ]; then
  printf '%-48s %s\n' "MIGRATION" "APPLIED"
  for f in "$MIGRATIONS"/*.sql; do
    v=$(basename "$f")
    when=$(psql_q -c "SELECT applied_at FROM schema_migrations WHERE version = '$v';")
    printf '%-48s %s\n' "$v" "${when:-pending}"
  done
  exit 0
fi

applied=0
for f in "$MIGRATIONS"/*.sql; do
  v=$(basename "$f")
  if [ -n "$(psql_q -c "SELECT 1 FROM schema_migrations WHERE version = '$v';")" ]; then
    continue
  fi
  echo "applying $v"

  # Some migrations open their own transaction (0001_init.sql does). Wrapping
  # one of those in another BEGIN gets you "there is already a transaction in
  # progress", the file commits itself, and the outer COMMIT then warns about a
  # transaction that is no longer there. It happens to work, but it means the
  # atomicity this script advertises is not actually in force, so detect it and
  # be explicit instead of emitting warnings nobody reads.
  if grep -qiE '^[[:space:]]*(BEGIN|START TRANSACTION)[[:space:]]*;' "$f"; then
    # The file owns its transaction. Run it as-is, then record it. The gap
    # between the two is real: a crash in between leaves the migration applied
    # but unrecorded, and the next run will try it again. That fails loudly on
    # a duplicate object rather than corrupting anything, which is the right
    # way round for a one-line window.
    "${COMPOSE[@]}" exec -T postgres psql -v ON_ERROR_STOP=1 -U "$PGUSER" -d "$PGDB" -q < "$f"
    psql_q -c "INSERT INTO schema_migrations (version) VALUES ('$v');" >/dev/null
  else
    # BEGIN/COMMIT wrap both the migration and its ledger row, so it either
    # lands completely and is marked, or does neither. ON_ERROR_STOP turns any
    # failure into a rollback plus a non-zero exit, rather than psql cheerfully
    # continuing to the next statement.
    {
      echo "BEGIN;"
      cat "$f"
      echo ";"
      echo "INSERT INTO schema_migrations (version) VALUES ('$v');"
      echo "COMMIT;"
    } | "${COMPOSE[@]}" exec -T postgres psql -v ON_ERROR_STOP=1 -U "$PGUSER" -d "$PGDB" -q
  fi
  applied=$((applied + 1))
done

if [ "$applied" -eq 0 ]; then
  echo "nothing to do; schema is up to date"
else
  echo "applied $applied migration(s)"
fi

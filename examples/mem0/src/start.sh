#!/bin/bash
set -euo pipefail

data=${MEM0_DIR:-/var/lib/mem0}
mkdir -p "$data"
# Hold the shared mount's lock until every service stops. Lock before
# inspecting PG_VERSION: PostgreSQL's own lock comes too late for initdb.
exec 9>"$data/server.lock"
flock --nonblock --conflict-exit-code 75 9 || {
    status=$?
    if [ "$status" -eq 75 ]; then
        printf 'Mem0 is already running against %s\n' "$data" >&2
        exit 0
    fi
    exit "$status"
}

export PGDATA=${PGDATA:-"$data/postgres"}
export POSTGRES_HOST=${POSTGRES_HOST:-127.0.0.1}
export POSTGRES_PORT=${POSTGRES_PORT:-5432}
export POSTGRES_DB=${POSTGRES_DB:-postgres}
export POSTGRES_USER=${POSTGRES_USER:-agent}
export POSTGRES_PASSWORD=${POSTGRES_PASSWORD:-local-only}
export APP_DB_NAME=${APP_DB_NAME:-mem0_app}
mkdir -p "$PGDATA"
chmod 0700 "$PGDATA"

cleanup() {
    trap - EXIT INT TERM
    [ -z "${api_pid:-}" ] || kill "$api_pid" 2>/dev/null || true
    [ -z "${dashboard_pid:-}" ] || kill "$dashboard_pid" 2>/dev/null || true
    [ -z "${api_pid:-}" ] || wait "$api_pid" 2>/dev/null || true
    [ -z "${dashboard_pid:-}" ] || wait "$dashboard_pid" 2>/dev/null || true
    pg_ctl -D "$PGDATA" -m fast stop >/dev/null 2>&1 || true
}
# A signal must exit, even when it interrupts an initialization command
# that subsequently succeeds. EXIT performs the cleanup exactly once.
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [ ! -s "$PGDATA/PG_VERSION" ]; then
    initdb --username="$POSTGRES_USER" --encoding=UTF8 --locale=C.UTF-8 \
        --auth-local=trust --auth-host=trust
fi

postgres_log="$data/postgres.log"
pg_ctl -D "$PGDATA" -l "$postgres_log" \
    -o "-h 127.0.0.1 -k $data -p $POSTGRES_PORT" start

for _ in $(seq 1 60); do
    pg_isready -q -h "$POSTGRES_HOST" -p "$POSTGRES_PORT" \
        -U "$POSTGRES_USER" -d "$POSTGRES_DB" && break
    sleep 1
done
pg_isready -q -h "$POSTGRES_HOST" -p "$POSTGRES_PORT" \
    -U "$POSTGRES_USER" -d "$POSTGRES_DB"

psql -v ON_ERROR_STOP=1 -h "$POSTGRES_HOST" -p "$POSTGRES_PORT" \
    -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v app_db="$APP_DB_NAME" <<'SQL'
CREATE EXTENSION IF NOT EXISTS vector;
SELECT format('CREATE DATABASE %I', :'app_db')
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = :'app_db')\gexec
SQL

jwt_file="$data/jwt-secret"
if [ ! -s "$jwt_file" ]; then
    umask 077
    python -c 'import secrets; print(secrets.token_urlsafe(64))' >"$jwt_file"
fi
export JWT_SECRET
JWT_SECRET=$(cat "$jwt_file")
# MCP is intentionally unauthenticated at its transport boundary, as in
# the prior Kit. Its adapter uses this private key to call the official
# authenticated API over loopback.
export ADMIN_API_KEY=$JWT_SECRET

cd /opt/mem0/server
alembic upgrade head
uvicorn app:app --host 0.0.0.0 --port 8000 &
api_pid=$!

cd /opt/mem0/dashboard
printenv | grep '^NEXT_PUBLIC_' | while IFS='=' read -r key value; do
    escaped=$(printf '%s' "$value" | sed -e 's/[\\&|]/\\&/g')
    find .next -type f -exec sed -i "s|$key|$escaped|g" {} \;
done
HOSTNAME=0.0.0.0 PORT=3000 node server.js &
dashboard_pid=$!

wait -n "$api_pid" "$dashboard_pid"

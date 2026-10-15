#!/usr/bin/env bash
set -euo pipefail

API_URL="${API_URL:-http://127.0.0.1:8080}"
DB_CONTAINER="${DB_CONTAINER:-avxxx-db-1}"

echo "checking API health: ${API_URL}/api/health"
health="$(curl --fail --silent --show-error --max-time 10 "${API_URL}/api/health")"
case "$health" in
  *'"ok":true'*) ;;
  *) echo "unexpected health response: $health" >&2; exit 1 ;;
esac

echo "checking public catalog endpoints"
curl --fail --silent --show-error --max-time 10 "${API_URL}/api/performers?limit=1" >/dev/null
curl --fail --silent --show-error --max-time 10 "${API_URL}/api/releases?limit=1" >/dev/null

if command -v docker >/dev/null 2>&1 && docker inspect "$DB_CONTAINER" >/dev/null 2>&1; then
  echo "checking PostgreSQL container: ${DB_CONTAINER}"
  docker exec "$DB_CONTAINER" pg_isready -U "${POSTGRES_USER:-avxxx}" -d "${POSTGRES_DB:-avxxx}" >/dev/null
fi

echo "smoke checks passed"

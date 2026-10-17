#!/usr/bin/env bash
# Run Go integration tests against a disposable Postgres 17 container.
#
# Integration tests drop the public schema of AVXXX_TEST_DATABASE_URL, so this
# script never uses DATABASE_URL or the docker compose `db` service: it starts a
# uniquely named, --rm container on a random localhost port and always removes it.
set -euo pipefail

cd "$(dirname "$0")/.."

image="${AVXXX_TEST_PG_IMAGE:-postgres:17}"
name="avxxx-test-pg-$$-$(date +%s)"
user=avxxx_test
password=avxxx_test
db=avxxx_test

cleanup() {
	docker rm -f -v "$name" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

echo "Starting disposable Postgres container $name ($image)..."
docker run -d --rm --name "$name" \
	-e POSTGRES_USER="$user" \
	-e POSTGRES_PASSWORD="$password" \
	-e POSTGRES_DB="$db" \
	-p 127.0.0.1::5432 \
	"$image" >/dev/null

port="$(docker port "$name" 5432/tcp | head -n1 | sed 's/.*://')"
if [ -z "$port" ]; then
	echo "could not determine host port for $name" >&2
	exit 1
fi

echo "Waiting for Postgres on 127.0.0.1:$port..."
ready=0
for _ in $(seq 1 60); do
	# Check over TCP so we don't catch the init-phase server (unix socket only).
	if docker exec "$name" pg_isready -h 127.0.0.1 -U "$user" -d "$db" >/dev/null 2>&1; then
		ready=1
		break
	fi
	sleep 1
done
if [ "$ready" -ne 1 ]; then
	echo "Postgres did not become ready in time" >&2
	docker logs "$name" >&2 || true
	exit 1
fi

export AVXXX_TEST_DATABASE_URL="postgres://$user:$password@127.0.0.1:$port/$db?sslmode=disable"
echo "Running integration tests (AVXXX_TEST_DATABASE_URL -> 127.0.0.1:$port/$db)..."
# Unset DATABASE_URL so nothing can fall back to a real database.
# AVXXX_TEST_PKGS narrows the packages (make acceptance: ./internal/acceptance/).
# shellcheck disable=SC2086
env -u DATABASE_URL go test ${AVXXX_TEST_PKGS:-./...} -run Integration -count=1 "$@"

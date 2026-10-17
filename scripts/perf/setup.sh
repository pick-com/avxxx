#!/usr/bin/env bash
# Performance-test database (docs/PERFORMANCE.md): start a disposable
# postgres:17 container, apply the migrations (by booting the API once), seed
# it with scripts/perf/seed.sql and print the connection URL.
#
# Defaults are SMALL (1k performers, 5k releases, 1k users; ~150k rows,
# < 200 MB). Production volume (PERF_PERFORMERS=50000 PERF_RELEASES=300000
# PERF_USERS=200000) needs SEVERAL GB of free Docker disk (data + WAL):
# the script refuses to seed more than the defaults unless the Docker VM
# has PERF_MIN_FREE_GB (default 15) free, or PERF_FORCE=1.
#
# It never reads or writes DATABASE_URL, the compose db service or any other
# container: the container is named $PERF_DB_NAME (default avxxx-perf-db) on a
# random localhost port. Remove it WITH its anonymous data volume when done:
#
#   docker rm -f -v avxxx-perf-db
#
# (plain `docker rm -f` leaves the data volume behind).
#
#   scripts/perf/setup.sh                                    # small
#   PERF_PERFORMERS=50000 PERF_RELEASES=300000 PERF_USERS=200000 scripts/perf/setup.sh   # full
set -euo pipefail
unset DATABASE_URL PGHOST PGPORT PGUSER PGPASSWORD PGDATABASE PGSERVICE
cd "$(dirname "$0")/../.."

performers="${PERF_PERFORMERS:-1000}"
releases="${PERF_RELEASES:-5000}"
users="${PERF_USERS:-1000}"
name="${PERF_DB_NAME:-avxxx-perf-db}"

# Free space of the Docker VM's root filesystem, in GB.
free_gb="$(docker run --rm --entrypoint df alpine:3 -k / | awk 'NR==2 {print int($4/1024/1024)}')"
echo "Docker disk: ${free_gb} GB free"
if [ "${PERF_FORCE:-0}" != 1 ] && { [ "$performers" -gt 1000 ] || [ "$releases" -gt 5000 ] || [ "$users" -gt 1000 ]; } &&
	[ "$free_gb" -lt "${PERF_MIN_FREE_GB:-15}" ]; then
	echo "refusing to seed $performers performers / $releases releases / $users users with ${free_gb} GB free" >&2
	echo "(needs PERF_MIN_FREE_GB=${PERF_MIN_FREE_GB:-15}; PERF_FORCE=1 overrides)" >&2
	exit 1
fi

if docker inspect "$name" >/dev/null 2>&1; then
	echo "container $name already exists; docker rm -f -v $name first" >&2
	exit 1
fi
# Default postgres settings (as in docker-compose.yml, including its
# shm_size) except a larger WAL, so a big seed does not checkpoint
# constantly; it does not change plans.
docker run -d --rm --name "$name" --shm-size=256m -e POSTGRES_USER=perf -e POSTGRES_PASSWORD=perf -e POSTGRES_DB=perf \
	-p 127.0.0.1::5432 postgres:17 -c max_wal_size=1GB -c maintenance_work_mem=256MB >/dev/null
port="$(docker port "$name" 5432/tcp | head -n1 | sed 's/.*://')"
url="postgres://perf:perf@127.0.0.1:$port/perf?sslmode=disable"
for _ in $(seq 1 60); do
	docker exec "$name" pg_isready -h 127.0.0.1 -U perf -d perf >/dev/null 2>&1 && break
	sleep 1
done

bin="$(mktemp -d "${TMPDIR:-/tmp}/avxxx-perf.XXXXXX")"
trap 'rm -rf "$bin"' EXIT
go build -o "$bin/api" ./cmd/api
apiport="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
DATABASE_URL="$url" API_ADDR="127.0.0.1:$apiport" METRICS_ADDR=off "$bin/api" >"$bin/api.log" 2>&1 &
pid=$!
for _ in $(seq 1 60); do
	curl -fsS "http://127.0.0.1:$apiport/api/health" >/dev/null 2>&1 && break
	sleep 1
done
kill "$pid"
wait "$pid" 2>/dev/null || true

# psql runs inside the container: no local client needed.
time docker exec -i "$name" psql -U perf -d perf -v ON_ERROR_STOP=1 \
	-v performers="$performers" -v releases="$releases" -v users="$users" \
	-f - <scripts/perf/seed.sql
echo
echo "seeded: $url"
echo "remove: docker rm -f -v $name"

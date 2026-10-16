#!/usr/bin/env bash
# Restore drill (docs/DEPLOYMENT.md 恢复演练): prove the latest backup restores
# into a working system, then throw everything away.
#
#   1. pick the newest avxxx-*.dump in BACKUP_DIR (default ./backups), or
#      BACKUP_FILE; with none (or DRILL_SEED=1) create one from a disposable,
#      migrated and seeded postgres:17;
#   2. restore it into a fresh disposable postgres:17 container
#      (pg_restore --exit-on-error inside the container: no host client, no
#      version skew);
#   3. compare every table's row count with the rows inside the dump;
#   4. start the API against the restored database (it applies any newer
#      forward-only migrations, as a deploy would), run scripts/smoke.sh and a
#      few catalog reads, and report tables the migrations changed;
#   5. tear down every container, network, process and temp file it made.
#
# It never reads or writes DATABASE_URL, the compose db service or any
# existing container: all names are avxxx-drill-<pid>-<time>-*.
#
#   BACKUP_FILE=path.dump   restore this dump
#   BACKUP_DIR=dir          where to look for the newest avxxx-*.dump (./backups)
#   DRILL_SEED=1            ignore existing dumps; seed a disposable database
#   DRILL_API=binary|image  run the API from `go build ./cmd/api` (default when
#                           Go is installed) or the avxxx-app:latest image
#                           (default otherwise, e.g. on the production host)
#   DRILL_IMAGE=postgres:17 database image (keep in step with docker-compose.yml)
#   DRILL_KEEP=1            leave the containers running for inspection
set -euo pipefail
unset DATABASE_URL PGHOST PGPORT PGUSER PGPASSWORD PGDATABASE PGSERVICE
export LC_ALL=C # sort / join agree on order

cd "$(dirname "$0")/.."
root="$(pwd)"

image="${DRILL_IMAGE:-postgres:17}"
app_image="${DRILL_APP_IMAGE:-avxxx-app:latest}"
id="avxxx-drill-$$-$(date +%s)"
net="$id-net"
src="$id-src"
dst="$id-dst"
pg_user=drill
pg_password="drill-$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
pg_db=avxxx
work="$(mktemp -d "${TMPDIR:-/tmp}/avxxx-drill.XXXXXX")"
api_pid=""
api_container=""
t0="$(date +%s)"

phase_start=0
phase() { phase_start="$(date +%s)"; printf '\n== %s\n' "$*"; }
phase_done() { printf '   done in %ss\n' "$(($(date +%s) - phase_start))"; timings+=("$1=$(($(date +%s) - phase_start))s"); }
timings=()

cleanup() {
	local code=$?
	if [[ -n "$api_pid" ]]; then kill "$api_pid" 2>/dev/null || true; wait "$api_pid" 2>/dev/null || true; fi
	if [[ "${DRILL_KEEP:-}" == 1 ]]; then
		echo "DRILL_KEEP=1: leaving $src $dst ${api_container:-} on network $net (docker rm -f them when done)"
	else
		[[ -n "$api_container" ]] && docker rm -f "$api_container" >/dev/null 2>&1
		docker rm -f "$src" "$dst" >/dev/null 2>&1 || true
		docker network rm "$net" >/dev/null 2>&1 || true
	fi
	rm -rf "$work"
	if [[ $code -eq 0 ]]; then
		printf '\nRESTORE DRILL PASSED in %ss (%s)\n' "$(($(date +%s) - t0))" "${timings[*]}"
	else
		printf '\nRESTORE DRILL FAILED (exit %s) after %ss\n' "$code" "$(($(date +%s) - t0))" >&2
	fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM

need() { command -v "$1" >/dev/null 2>&1 || { echo "restore-drill: $1 is required" >&2; exit 1; }; }
need docker
need curl

start_pg() { # name
	docker run -d --rm --name "$1" --network "$net" \
		-e POSTGRES_USER="$pg_user" -e POSTGRES_PASSWORD="$pg_password" -e POSTGRES_DB="$pg_db" \
		-p 127.0.0.1::5432 "$image" >/dev/null
	for _ in $(seq 1 60); do
		# Over TCP, so the init-phase server (unix socket only) does not count.
		if docker exec "$1" pg_isready -h 127.0.0.1 -U "$pg_user" -d "$pg_db" >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done
	echo "postgres in $1 did not become ready" >&2
	docker logs "$1" >&2 || true
	return 1
}
host_port() { docker port "$1" 5432/tcp | head -n1 | sed 's/.*://'; }
psql_in() { # container sql...
	local c="$1"; shift
	docker exec -i "$c" psql -X -q -v ON_ERROR_STOP=1 -U "$pg_user" -d "$pg_db" "$@"
}
free_port() {
	if command -v python3 >/dev/null 2>&1; then
		python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
	else
		echo $((20000 + RANDOM % 20000))
	fi
}

# ---- API runner (binary or image)
api_mode="${DRILL_API:-}"
if [[ -z "$api_mode" ]]; then
	if command -v go >/dev/null 2>&1; then api_mode=binary; else api_mode=image; fi
fi
case "$api_mode" in
binary)
	need go
	phase "build API binary"
	go build -o "$work/api" ./cmd/api
	phase_done build
	;;
image)
	docker image inspect "$app_image" >/dev/null 2>&1 || { echo "restore-drill: image $app_image not found (docker compose build api)" >&2; exit 1; }
	;;
*) echo "DRILL_API must be binary or image" >&2; exit 2 ;;
esac

api_port=""
start_api() { # container (database), log file
	local c="$1" log="$2"
	api_port="$(free_port)"
	if [[ "$api_mode" == binary ]]; then
		DATABASE_URL="postgres://$pg_user:$pg_password@127.0.0.1:$(host_port "$c")/$pg_db?sslmode=disable" \
			API_ADDR="127.0.0.1:$api_port" METRICS_ADDR=off ADMIN_TOKEN="" LOG_FORMAT=text LOG_LEVEL=warn \
			MEDIA_LINK_ON_DEMAND=false "$work/api" >"$log" 2>&1 &
		api_pid=$!
	else
		api_container="$id-api-$(date +%s)"
		docker run -d --rm --name "$api_container" --network "$net" -p "127.0.0.1:$api_port:8080" \
			-e DATABASE_URL="postgres://$pg_user:$pg_password@$c:5432/$pg_db?sslmode=disable" \
			-e API_ADDR=0.0.0.0:8080 -e METRICS_ADDR=off -e LOG_FORMAT=text -e LOG_LEVEL=warn -e MEDIA_LINK_ON_DEMAND=false \
			"$app_image" /usr/local/bin/api >/dev/null
	fi
	for _ in $(seq 1 90); do
		if curl -fsS --max-time 2 "http://127.0.0.1:$api_port/api/health" >/dev/null 2>&1; then
			return 0
		fi
		if [[ -n "$api_pid" ]] && ! kill -0 "$api_pid" 2>/dev/null; then break; fi
		sleep 1
	done
	echo "API did not become healthy" >&2
	if [[ -n "$api_pid" ]]; then cat "$log" >&2; else docker logs "$api_container" >&2 || true; fi
	return 1
}
stop_api() {
	if [[ -n "$api_pid" ]]; then kill "$api_pid" 2>/dev/null || true; wait "$api_pid" 2>/dev/null || true; api_pid=""; fi
	if [[ -n "$api_container" ]]; then docker rm -f "$api_container" >/dev/null 2>&1 || true; api_container=""; fi
}

docker network create "$net" >/dev/null

# ---- 1. the backup
dump=""
origin=""
if [[ "${DRILL_SEED:-}" != 1 ]]; then
	if [[ -n "${BACKUP_FILE:-}" ]]; then
		[[ -f "$BACKUP_FILE" ]] || { echo "BACKUP_FILE $BACKUP_FILE not found" >&2; exit 2; }
		dump="$BACKUP_FILE"
	else
		dir="${BACKUP_DIR:-./backups}"
		if [[ -d "$dir" ]]; then
			# Newest by name: avxxx-YYYYMMDDTHHMMSSZ.dump sorts by time.
			dump="$(find "$dir" -maxdepth 1 -type f -name 'avxxx-*.dump' | sort | tail -n1)"
		fi
	fi
	[[ -n "$dump" ]] && origin="backup $dump"
fi
if [[ -z "$dump" ]]; then
	phase "no backup to drill: seed a disposable database and back it up"
	start_pg "$src"
	start_api "$src" "$work/api-seed.log" # store.Open applies every migration
	stop_api
	psql_in "$src" <scripts/seed-demo.sql
	psql_in "$src" <<'SQL'
-- A catalog-shaped load: sources, performers, releases, identifiers, credits,
-- tags, source records with raw_data, field history, users.
INSERT INTO sources(name,url,adapter,source_kind,license_status) VALUES('drill-src','https://drill.invalid/','javdatabase','aggregator','review_required');
INSERT INTO performers(slug,canonical_name,birth_date,status)
  SELECT 'drill-performer-'||g,'Drill Performer '||g,date '1990-01-01'+g,'active' FROM generate_series(1,300) g;
INSERT INTO releases(slug,canonical_title,release_date,maker_name,status)
  SELECT 'drl-'||lpad(g::text,4,'0'),'Drill Release '||g,date '2024-01-01'+g,'Drill Maker',CASE WHEN g%10=0 THEN 'draft' ELSE 'published' END FROM generate_series(1,1000) g;
INSERT INTO release_identifiers(release_id,identifier_type,identifier_value,normalized_value,identifier_scope)
  SELECT id,'dvd_code',upper(slug),slug,'maker:drill-maker' FROM releases WHERE slug LIKE 'drl-%';
INSERT INTO release_performers(release_id,performer_id,billing_order,is_primary)
  SELECT r.id,p.id,0,true FROM releases r JOIN performers p ON p.slug='drill-performer-'||(1+(substr(r.slug,5)::int % 300)) WHERE r.slug LIKE 'drl-%';
INSERT INTO release_tags(release_id,tag,normalized_tag) SELECT id,'Drill','drill' FROM releases WHERE slug LIKE 'drl-%';
INSERT INTO source_records(source_id,entity_type,source_record_key,source_url,release_id,raw_data,parser_version,resolution_status)
  SELECT s.id,'release',r.slug,'https://drill.invalid/'||r.slug,r.id,jsonb_build_object('title',r.canonical_title,'note',E'multi\nline\ttext'),'drill','resolved'
  FROM releases r, sources s WHERE s.name='drill-src' AND r.slug LIKE 'drl-%';
INSERT INTO source_field_values(source_record_id,field_name,raw_value,normalized_value,captured_at)
  SELECT id,'title',raw_data->'title',raw_data->'title',now() FROM source_records WHERE parser_version='drill';
INSERT INTO users(email,password_hash) SELECT 'drill'||g||'@example.invalid','x' FROM generate_series(1,50) g;
SQL
	dump="$work/avxxx-drill-seed.dump"
	docker exec "$src" pg_dump --format=custom --no-owner --no-acl -U "$pg_user" -d "$pg_db" >"$dump"
	origin="seeded disposable database"
	phase_done seed
fi
printf 'drilling %s (%s bytes)\n' "$origin" "$(wc -c <"$dump" | tr -d ' ')"

# ---- 2. restore into a fresh database
phase "restore into a fresh $image"
start_pg "$dst"
docker exec -i "$dst" pg_restore --exit-on-error --no-owner --no-acl -U "$pg_user" -d "$pg_db" <"$dump"
phase_done restore

# ---- 3. row counts: restored tables vs the rows inside the dump
phase "compare row counts with the dump"
# COPY text format is one line per row (newlines are escaped), between the
# "COPY schema.table (...) FROM stdin;" header and the "\." terminator.
docker exec -i "$dst" pg_restore --data-only -f - <"$dump" | awk '
	/^COPY / { split($2, a, "."); t = (a[2] != "" ? a[2] : a[1]); n = 0; inside = 1; next }
	inside && $0 == "\\." { print t, n; inside = 0; next }
	inside { n++ }' | sort >"$work/dump.counts"
count_sql="SELECT format('SELECT %L AS t, count(*) AS n FROM %I.%I', c.relname, n.nspname, c.relname)
	FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.relkind IN ('r','p') AND n.nspname='public' ORDER BY 1"
counts() { # container -> "table count" lines
	psql_in "$1" -At -c "$count_sql" | {
		q=""
		while IFS= read -r line; do q+="${q:+ UNION ALL }$line"; done
		[[ -n "$q" ]] && psql_in "$1" -At -F' ' -c "$q"
	} | sort
}
counts "$dst" >"$work/restored.counts"
tables="$(wc -l <"$work/restored.counts" | tr -d ' ')"
rows="$(awk '{ s += $2 } END { print s + 0 }' "$work/restored.counts")"
# Every table in the dump must hold exactly its dumped rows.
mismatch="$(join -a1 -e MISSING -o 0,1.2,2.2 "$work/dump.counts" "$work/restored.counts" | awk '$2 != $3')"
if [[ -n "$mismatch" ]]; then
	echo "row counts differ (table dumped restored):" >&2
	echo "$mismatch" >&2
	exit 1
fi
if [[ "$origin" == "seeded disposable database" ]]; then
	counts "$src" >"$work/source.counts"
	if ! diff -u "$work/source.counts" "$work/restored.counts" >"$work/source.diff"; then
		echo "restored row counts differ from the source database:" >&2
		cat "$work/source.diff" >&2
		exit 1
	fi
	echo "restored counts match the live source database"
fi
printf '%s tables, %s rows restored; every table matches the dump\n' "$tables" "$rows"
for t in sources performers releases release_identifiers source_records users jobs; do
	grep -E "^$t " "$work/restored.counts" | awk '{ printf "   %-22s %s\n", $1, $2 }' || true
done
phase_done counts

# ---- 4. the API on the restored database
phase "start the API ($api_mode) on the restored database"
start_api "$dst" "$work/api.log"
echo "API healthy on 127.0.0.1:$api_port"
phase_done api

phase "smoke and catalog checks"
API_URL="http://127.0.0.1:$api_port" DB_CONTAINER="$dst" POSTGRES_USER="$pg_user" POSTGRES_DB="$pg_db" "$root/scripts/smoke.sh"
published="$(psql_in "$dst" -At -c "SELECT count(*) FROM releases WHERE status='published' AND merged_into_id IS NULL")"
if [[ "$published" -gt 0 ]]; then
	slug="$(psql_in "$dst" -At -c "SELECT slug FROM releases WHERE status='published' AND merged_into_id IS NULL ORDER BY id LIMIT 1")"
	curl -fsS --max-time 10 "http://127.0.0.1:$api_port/api/releases/$slug" | grep -q "\"$slug\"" || { echo "GET /api/releases/$slug did not return the release" >&2; exit 1; }
	curl -fsS --max-time 10 "http://127.0.0.1:$api_port/api/releases?limit=5" | grep -q '"slug"' || { echo "release list is empty although $published releases are published" >&2; exit 1; }
	echo "catalog reads OK ($published published releases; /api/releases/$slug)"
fi
# Forward-only migrations may have run on start-up (a backup older than the
# code): report which tables they changed. Not a failure.
counts "$dst" >"$work/after-api.counts"
if ! diff -q "$work/restored.counts" "$work/after-api.counts" >/dev/null; then
	echo "tables changed by start-up migrations (restored -> after API start):"
	join -a1 -a2 -e 0 -o 0,1.2,2.2 "$work/restored.counts" "$work/after-api.counts" | awk '$2 != $3 { printf "   %-22s %s -> %s\n", $1, $2, $3 }'
fi
phase_done smoke
stop_api

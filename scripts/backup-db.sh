#!/usr/bin/env bash
# PostgreSQL backup in custom format (docs/PRODUCTION_OPERATIONS.md 备份与恢复).
#
#   DATABASE_URL         database to dump with the host pg_dump
#   BACKUP_VIA_COMPOSE=1 run pg_dump inside the compose `db` container instead
#                        (no host client needed, versions always match); uses
#                        POSTGRES_USER / POSTGRES_DB (default avxxx)
#   OUTPUT_DIR           where dumps go (default ./backups)
#   BACKUP_KEEP_DAYS     delete avxxx-*.dump older than N days after a
#                        successful backup (default: keep everything)
#   BACKUP_METRICS_DIR   node_exporter textfile directory; on success the
#                        script writes avxxx_backup.prom there (empty = off).
#                        The monitoring profile mounts ./backups/metrics.
set -euo pipefail
umask 077  # dumps contain the whole catalog and user data

DATABASE_URL="${DATABASE_URL:-postgres://avxxx:avxxx@127.0.0.1:55434/avxxx?sslmode=disable}"
OUTPUT_DIR="${OUTPUT_DIR:-./backups}"
BACKUP_METRICS_DIR="${BACKUP_METRICS_DIR:-}"
BACKUP_KEEP_DAYS="${BACKUP_KEEP_DAYS:-}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$OUTPUT_DIR"
output="${OUTPUT_DIR%/}/avxxx-${STAMP}.dump"
partial="${output}.partial"
trap 'rm -f "$partial"' EXIT

start="$(date +%s)"
if [[ "${BACKUP_VIA_COMPOSE:-}" == 1 ]]; then
  docker compose exec -T db pg_dump --format=custom --no-owner --no-acl \
    -U "${POSTGRES_USER:-avxxx}" -d "${POSTGRES_DB:-avxxx}" >"$partial"
else
  command -v pg_dump >/dev/null 2>&1 || { echo "pg_dump is required (or set BACKUP_VIA_COMPOSE=1)" >&2; exit 1; }
  pg_dump --format=custom --no-owner --no-acl --file="$partial" "$DATABASE_URL"
fi
# Only a complete dump gets the final name; a failed run leaves nothing behind.
mv "$partial" "$output"
end="$(date +%s)"
size="$(wc -c <"$output" | tr -d ' ')"
printf 'backup written to %s (%s bytes)\n' "$output" "$size"

if [[ -n "$BACKUP_KEEP_DAYS" ]]; then
  find "$OUTPUT_DIR" -maxdepth 1 -type f -name 'avxxx-*.dump' -mtime +"$BACKUP_KEEP_DAYS" -print -delete
fi

if [[ -n "$BACKUP_METRICS_DIR" ]]; then
  # node_exporter runs as nobody: the directory and file must be world-readable.
  mkdir -p -m 0755 "$BACKUP_METRICS_DIR"
  prom="${BACKUP_METRICS_DIR%/}/avxxx_backup.prom"
  # Written to a temp file and renamed so node_exporter never reads half a file.
  tmp="$(mktemp "${prom}.XXXXXX")"
  cat >"$tmp" <<EOF
# HELP avxxx_backup_last_success_timestamp_seconds Unix time the last successful database backup finished.
# TYPE avxxx_backup_last_success_timestamp_seconds gauge
avxxx_backup_last_success_timestamp_seconds ${end}
# HELP avxxx_backup_last_size_bytes Size of the last successful database backup.
# TYPE avxxx_backup_last_size_bytes gauge
avxxx_backup_last_size_bytes ${size}
# HELP avxxx_backup_last_duration_seconds Duration of the last successful database backup.
# TYPE avxxx_backup_last_duration_seconds gauge
avxxx_backup_last_duration_seconds $((end - start))
EOF
  chmod 0644 "$tmp"
  mv "$tmp" "$prom"
fi

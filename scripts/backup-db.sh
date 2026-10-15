#!/usr/bin/env bash
set -euo pipefail

DATABASE_URL="${DATABASE_URL:-postgres://avxxx:avxxx@127.0.0.1:55434/avxxx?sslmode=disable}"
OUTPUT_DIR="${OUTPUT_DIR:-./backups}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$OUTPUT_DIR"
output="${OUTPUT_DIR%/}/avxxx-${STAMP}.dump"

command -v pg_dump >/dev/null 2>&1 || { echo "pg_dump is required" >&2; exit 1; }
pg_dump --format=custom --no-owner --no-acl --file="$output" "$DATABASE_URL"
printf 'backup written to %s\n' "$output"

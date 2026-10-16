#!/usr/bin/env bash
# Validate the Prometheus config and alert rules with promtool. Uses a local
# promtool when installed, otherwise the image the monitoring profile runs.
set -euo pipefail

cd "$(dirname "$0")/.."

image="${PROMETHEUS_IMAGE:-prom/prometheus:v3.5.0}"

if command -v promtool >/dev/null 2>&1; then
	promtool check rules deploy/monitoring/alerts.yml
	(cd deploy/monitoring && promtool test rules alerts_test.yml)
	exit 0
fi

docker run --rm --entrypoint /bin/promtool \
	-v "$PWD/deploy/monitoring:/etc/prometheus:ro" \
	"$image" check rules /etc/prometheus/alerts.yml
docker run --rm --entrypoint /bin/promtool -w /etc/prometheus \
	-v "$PWD/deploy/monitoring:/etc/prometheus:ro" \
	"$image" test rules alerts_test.yml
# check config resolves rule_files relative to the mounted directory.
docker run --rm --entrypoint /bin/promtool \
	-v "$PWD/deploy/monitoring:/etc/prometheus:ro" \
	"$image" check config --syntax-only /etc/prometheus/prometheus.yml

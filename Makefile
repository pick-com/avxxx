ifneq (,$(wildcard .env))
include .env
export
endif
.PHONY: db-up seed-demo api scheduler scraper audit notifier web web-start seo-check test test-integration acceptance fixturecheck build smoke backup restore-drill check-alerts monitoring
db-up:
	docker compose up -d db
seed-demo:
	psql "$${DATABASE_URL:-postgres://avxxx:avxxx@127.0.0.1:55434/avxxx?sslmode=disable}" -f scripts/seed-demo.sql
api:
	go run ./cmd/api
scraper:
	go run ./cmd/scraper
scheduler:
	go run ./cmd/scheduler
audit:
	go run ./cmd/audit
notifier:
	go run ./cmd/notifier
web:
	cd web && npm run dev
# Production web server (SSR) on :3000 after a build; API_INTERNAL_URL defaults to http://127.0.0.1:8080.
web-start:
	cd web && npm run build && npm start
# 收录健康分 inputs: WEB_URL / API_URL / SEO_REPORT_TOKEN (ADMIN_TOKEN fallback) (see web/scripts/seo-check.mjs).
seo-check:
	cd web && npm run seo:check
test:
	go test ./...
# Disposable postgres:17 container (random port, --rm); never touches DATABASE_URL or compose db.
test-integration:
	./scripts/test-integration.sh
# End-to-end acceptance suite only (re-crawl dedupe, blocking, overrides/merges/splits, public API leaks).
acceptance:
	AVXXX_TEST_PKGS=./internal/acceptance/ ./scripts/test-integration.sh -v
# Parser acceptance over recorded fixtures (no network); add -strict to enforce 20/100 per source.
fixturecheck:
	go run ./cmd/fixturecheck
build:
	go build ./cmd/...
	cd web && npm run build
smoke:
	./scripts/smoke.sh
backup:
	./scripts/backup-db.sh
# Restore the latest backup (or a seeded disposable DB's) into a throw-away postgres:17,
# boot the API on it, run smoke + row-count checks, tear down. Never touches DATABASE_URL.
restore-drill:
	./scripts/restore-drill.sh
# promtool check + unit tests, amtool check-config + routing tests (docker, or local promtool/amtool).
check-alerts:
	./scripts/check-alerts.sh
# Prometheus 127.0.0.1:9095, Alertmanager 127.0.0.1:9096, node_exporter (compose profile "monitoring").
monitoring:
	docker compose --profile monitoring up -d prometheus alertmanager node-exporter

ifneq (,$(wildcard .env))
include .env
export
endif
.PHONY: db-up seed-demo api scheduler scraper audit notifier web test test-integration fixturecheck build smoke backup
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
test:
	go test ./...
# Disposable postgres:17 container (random port, --rm); never touches DATABASE_URL or compose db.
test-integration:
	./scripts/test-integration.sh
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

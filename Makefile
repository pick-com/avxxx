ifneq (,$(wildcard .env))
include .env
export
endif
.PHONY: db-up api scheduler scraper web test build
db-up:
	docker compose up -d db
api:
	go run ./cmd/api
scraper:
	go run ./cmd/scraper
scheduler:
	go run ./cmd/scheduler
web:
	cd web && npm run dev
test:
	go test ./...
build:
	go build ./cmd/...
	cd web && npm run build

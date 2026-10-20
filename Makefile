.PHONY: build test race lint up down smoke seed load bench fmt

build:
	go build -o bin/ledgerbot ./cmd/ledgerbot

test:
	go test ./...

race:
	LEDGERBOT_TEST_DATABASE_URL=postgres://ledgerbot:ledgerbot@localhost:5432/ledgerbot?sslmode=disable go test -race ./...

bench:
	go test -run '^$$' -bench . -benchmem ./internal/...

lint:
	gofmt -l . && go vet ./... && golangci-lint run

fmt:
	gofmt -w .

up:
	docker compose up -d --build

down:
	docker compose down

smoke:
	./scripts/smoke.sh

seed:
	go run ./cmd/seedgen -n 4000 -out loadtest/seed

load: seed
	docker compose -f compose.yml -f loadtest/compose.loadtest.yml up -d --build
	docker run --rm -i -v "$$PWD/loadtest:/scripts" grafana/k6:1.3.0 run -e BASE_URL=http://host.docker.internal:8080 /scripts/k6.js

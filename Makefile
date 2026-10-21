.PHONY: build test race lint up down smoke seed load bench fmt heavy perf report

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

# Every suite + benchmarks + 2/4/6/8 GB load test, then reports/index.html.
heavy:
	./scripts/heavy-test.sh

# Only the RAM-profile load test (raw data in reports/raw/perf).
perf:
	./scripts/perf/run-profiles.sh

report:
	go run ./cmd/testreport -in reports/raw -out reports

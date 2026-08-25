.PHONY: all verify generate contract test test-race vet build web-check compose-config up down

all: contract test build

verify: contract test-race vet build web-check compose-config

generate:
	buf generate

contract:
	buf format --diff --exit-code
	buf lint
	buf build

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

build:
	go build ./cmd/...

web-check:
	cd web && npm ci --no-audit --no-fund
	cd web && npm run lint
	cd web && npm test
	cd web && npm run build

compose-config:
	docker compose --env-file deploy/compose/.env.example -f deploy/compose/docker-compose.yml config --quiet

up:
	docker compose --env-file deploy/compose/.env.example -f deploy/compose/docker-compose.yml up --build

down:
	docker compose --env-file deploy/compose/.env.example -f deploy/compose/docker-compose.yml down

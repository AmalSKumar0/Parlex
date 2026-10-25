.PHONY: all build run test lint migrate sqlc sim docker

all: lint test build

build:
	go build -o bin/server ./cmd/server
	go build -o bin/simulator ./cmd/simulator

run: build
	./bin/server

test:
	go test -race -v ./...

lint:
	golangci-lint run ./...

migrate:
	go run ./cmd/server -migrate

sqlc:
	sqlc generate

sim:
	go run ./cmd/simulator

docker:
	docker build -t parlex:latest -f deploy/Dockerfile .

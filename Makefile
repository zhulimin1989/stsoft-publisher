.PHONY: build build-token test lint run new-tokens tidy image up down fmt

build:
	go build -o bin/publisher ./cmd/publisher
	go build -o bin/publisher-token ./cmd/publisher-token

build-token:
	go build -o bin/publisher-token ./cmd/publisher-token

fmt:
	gofmt -w ./cmd ./internal

test:
	go test -count=1 ./...

lint:
	golangci-lint run ./...

run:
	go run ./cmd/publisher --config config/publisher.cfg

new-tokens:
	@go run ./cmd/publisher-token -count $${COUNT:-4} -prefix $${PREFIX:-pubtok}

tidy:
	go mod tidy

image:
	docker build -t tokenplatform-publisher:local .

up:
	docker compose -f deploy/docker-compose.yml up -d postgres redis

down:
	docker compose -f deploy/docker-compose.yml down

.PHONY: build run test clean

APP_NAME := free-llm-hack-proxy

build:
	go build -o bin/$(APP_NAME) ./cmd/

run:
	go run ./cmd/

test:
	go test ./...

clean:
	rm -rf bin/

fmt:
	go fmt ./...

vet:
	go vet ./...

lint: fmt vet

all: build test

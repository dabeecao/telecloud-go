.PHONY: build run test clean fmt tidy frontend docker-build help

DEFAULT_API_ID ?= $(shell grep -E "^API_ID=" .env 2>/dev/null | cut -d= -f2)
DEFAULT_API_HASH ?= $(shell grep -E "^API_HASH=" .env 2>/dev/null | cut -d= -f2)

ifeq ($(DEFAULT_API_ID),)
DEFAULT_API_ID := 0
endif

VERSION ?= $(shell sed -n 's/.*version *= *"\([^"]*\)".*/\1/p' main.go 2>/dev/null || echo "dev")

# Default goal when running only 'make'
.DEFAULT_GOAL := help

help:
	@echo "Available commands in Makefile:"
	@echo "  make build          Build Go application (injecting default API credentials)"
	@echo "  make run            Build and run application locally"
	@echo "  make frontend       Build frontend assets in web directory"
	@echo "  make test           Run unit tests for all packages"
	@echo "  make fmt            Format Go source code"
	@echo "  make tidy           Tidy Go modules"
	@echo "  make clean          Clean build artifacts and temporary files"
	@echo "  make docker-build   Build Docker image locally with default API credentials"

build:
	@echo "===> Building TeleCloud $(VERSION) with default API credentials..."
	go build -ldflags="-s -w -X main.version=$(VERSION) -X telecloud/config.DefaultAPIIDStr=$(DEFAULT_API_ID) -X telecloud/config.DefaultAPIHash=$(DEFAULT_API_HASH)" -o telecloud
	@echo "===> Build completed! Output: ./telecloud"

run: build
	@echo "===> Running TeleCloud..."
	./telecloud

frontend:
	@echo "===> Building frontend assets..."
	@cd web && chmod +x build-frontend.sh && ./build-frontend.sh

test:
	@echo "===> Running tests..."
	go test ./...

fmt:
	@echo "===> Formatting Go code..."
	go fmt ./...

tidy:
	@echo "===> Running go mod tidy..."
	go mod tidy

clean:
	@echo "===> Cleaning build artifacts..."
	rm -f telecloud telecloud.exe
	rm -rf build/

docker-build:
	@echo "===> Building Docker Image with default API credentials..."
	docker build --build-arg DEFAULT_API_ID=$(DEFAULT_API_ID) --build-arg DEFAULT_API_HASH=$(DEFAULT_API_HASH) -t telecloud:local .

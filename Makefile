.PHONY: build run test test-race cover vet fmt fmt-check check clean tidy frontend docker-build help

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
	@echo "  make test-race      Run unit tests with the race detector"
	@echo "  make cover          Run tests and report per-function coverage"
	@echo "  make vet            Run go vet on all packages"
	@echo "  make fmt            Format Go source code (gofmt -w)"
	@echo "  make fmt-check      Verify Go source is gofmt-formatted (no writes)"
	@echo "  make check          Run the full local CI gate: fmt-check + vet + test"
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

test-race:
	@echo "===> Running tests with race detector..."
	go test -race -count=1 ./...

cover:
	@echo "===> Running tests with coverage..."
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -n 1
	@echo "===> Full profile written to coverage.out (view with: go tool cover -html=coverage.out)"

vet:
	@echo "===> Running go vet..."
	go vet ./...

fmt:
	@echo "===> Formatting Go code..."
	gofmt -w $$(git ls-files '*.go' | grep -v '^web/' || find . -name '*.go' -not -path './web/*' -not -path './.git/*')

fmt-check:
	@echo "===> Checking gofmt..."
	@unformatted="$$(gofmt -l . | grep -v '^web/' | grep -v '^video-cong-nghe/' || true)"; \
	if [ -n "$$unformatted" ]; then \
		echo "The following files are not gofmt-formatted:"; \
		echo "$$unformatted"; \
		echo "Run 'make fmt' and commit the result."; \
		exit 1; \
	fi; \
	echo "All Go files are gofmt-formatted."

check: fmt-check vet test
	@echo "===> All local CI checks passed."

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

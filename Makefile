.PHONY: help build build-xk6 run test docker-build docker-up docker-down clean

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the synthetics service binary
	@echo "Building synthetics service..."
	go build -o synthetics ./cmd/synthetics
	@echo "Done! Binary: ./synthetics"

build-xk6: ## Build custom k6 binary with Storj extension
	@echo "Building custom k6 binary..."
	./scripts/build-xk6.sh

run: ## Run the synthetics service locally
	@echo "Starting synthetics service..."
	go run cmd/synthetics/main.go

test: ## Run Go tests
	@echo "Running tests..."
	go test -v ./...

docker-build: ## Build Docker images (uses BuildKit for caching)
	@echo "Building Docker images with BuildKit..."
	DOCKER_BUILDKIT=1 COMPOSE_DOCKER_CLI_BUILD=1 docker-compose -f deployments/docker-compose.yml build

docker-up: ## Start Docker stack (Prometheus + Grafana + Synthetics)
	@echo "Starting Docker stack..."
	docker-compose -f deployments/docker-compose.yml up -d
	@echo ""
	@echo "Services available at:"
	@echo "  Synthetics:  http://localhost:8080/metrics"
	@echo "  Prometheus:  http://localhost:9090"
	@echo "  Grafana:     http://localhost:3000 (admin/admin)"

docker-down: ## Stop Docker stack
	@echo "Stopping Docker stack..."
	docker-compose -f deployments/docker-compose.yml down

docker-logs: ## View Docker logs
	docker-compose -f deployments/docker-compose.yml logs -f

docker-buildx-setup: ## Setup Docker buildx for multi-arch builds
	@echo "Setting up Docker buildx..."
	docker buildx create --name synthetics-builder --use --bootstrap || docker buildx use synthetics-builder
	docker buildx inspect --bootstrap

docker-buildx: docker-buildx-setup ## Build Docker image for current platform (loads locally)
	@echo "Building Docker image for current platform..."
	docker buildx build \
		--file deployments/Dockerfile \
		--tag ghcr.io/ethanadams/synthetics:latest \
		--load \
		.

docker-buildx-push: ## Build and push multi-arch image to GHCR (VERSION=x.x.x or uses VERSION file)
	@./scripts/docker-build-push.sh $(VERSION)

docker-buildx-dry: ## Test build for current platform without pushing (VERSION=x.x.x or uses VERSION file)
	@DRY_RUN=1 ./scripts/docker-build-push.sh $(VERSION)

docker-build-k6-base: ## Build k6 base image (one-time, ~10 min) - rebuild only when extension changes
	@echo "Building k6 base image (this takes ~10 minutes but only needs to be done once)..."
	DOCKER_BUILDKIT=1 docker build \
		--file deployments/Dockerfile.k6-base \
		--tag ghcr.io/ethanadams/synthetics-k6:latest \
		--progress=plain \
		.
	@echo "Done! k6 base image ready."

docker-build-fast: docker-build-k6-base ## Fast build using pre-built k6 base (~30 seconds)
	@echo "Building synthetics service (fast mode)..."
	DOCKER_BUILDKIT=1 docker build \
		--file deployments/Dockerfile.fast \
		--tag ghcr.io/ethanadams/synthetics:latest \
		--progress=plain \
		.
	@echo "Done! Build completed in <1 minute."

clean: ## Clean build artifacts
	@echo "Cleaning..."
	rm -f synthetics k6
	rm -f /tmp/k6-output-*.json
	go clean

fmt: ## Format Go code
	@echo "Formatting code..."
	go fmt ./...

lint: ## Run linter
	@echo "Running linter..."
	golangci-lint run

deps: ## Download Go dependencies
	@echo "Downloading dependencies..."
	go mod download
	go mod tidy

install-xk6: ## Install xk6 tool
	@echo "Installing xk6..."
	go install go.k6.io/xk6/cmd/xk6@latest
	@echo "Done! xk6 installed to $(shell go env GOPATH)/bin/xk6"

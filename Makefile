.DEFAULT_GOAL := help

GO            ?= go
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT        ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS       := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)
CGO_ENABLED   ?= 0
BIN_DIR       := bin

CONTROL_PLANE := $(BIN_DIR)/control-plane
BRIDGE        := $(BIN_DIR)/helmdeck-mcp

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS=":.*##"; printf "Usage: make \033[36m<target>\033[0m\n\nTargets:\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

.PHONY: build
build: $(CONTROL_PLANE) $(BRIDGE) ## Build all binaries

$(CONTROL_PLANE): $(shell find cmd/control-plane internal -name '*.go' 2>/dev/null) go.mod
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $@ ./cmd/control-plane

$(BRIDGE): $(shell find cmd/helmdeck-mcp -name '*.go' 2>/dev/null) go.mod
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $@ ./cmd/helmdeck-mcp

.PHONY: test
test: ## Run unit tests
	$(GO) test -race -count=1 ./...

.PHONY: vet
vet: ## go vet
	$(GO) vet ./...

.PHONY: fmt
fmt: ## gofmt -w
	gofmt -w cmd internal

.PHONY: tidy
tidy: ## go mod tidy
	$(GO) mod tidy

.PHONY: run
run: $(CONTROL_PLANE) ## Run control-plane locally on :3000
	HELMDECK_ADDR=:3000 $(CONTROL_PLANE)

.PHONY: smoke
smoke: build ## End-to-end smoke (T111 — placeholder until session runtime lands)
	@echo "TODO(T111): bring up compose stack and run navigate→screenshot→delete flow"

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)

.PHONY: ci
ci: vet test build ## CI pipeline (vet + test + build)

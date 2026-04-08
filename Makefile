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

.PHONY: web-deps
web-deps: ## Install Management UI npm dependencies (T601)
	cd web && npm install --no-audit --no-fund

.PHONY: web-build
web-build: ## Build the embedded Management UI bundle into web/dist (T601)
	cd web && npm run build

.PHONY: web-dev
web-dev: ## Start the Vite dev server (proxies /api to http://localhost:3000)
	cd web && npm run dev

.PHONY: web-clean
web-clean: ## Remove web/node_modules and the built dist (preserves placeholder)
	rm -rf web/node_modules web/dist
	mkdir -p web/dist
	@echo "// regenerate placeholder via make web-build" > web/dist/index.html
	@echo "Run 'make web-build' to regenerate the dist bundle."

.PHONY: run
run: $(CONTROL_PLANE) ## Run control-plane locally on :3000
	HELMDECK_ADDR=:3000 $(CONTROL_PLANE)

.PHONY: sidecar-build
sidecar-build: ## Build the browser sidecar image locally as helmdeck-sidecar:dev
	docker build -f deploy/docker/sidecar.Dockerfile -t helmdeck-sidecar:dev .

.PHONY: sidecar-python-build
sidecar-python-build: sidecar-build ## Build the Python language sidecar (depends on the base sidecar)
	docker build -f deploy/docker/sidecar-python.Dockerfile \
		--build-arg BASE_IMAGE=helmdeck-sidecar:dev \
		-t helmdeck-sidecar-python:dev .

.PHONY: sidecar-node-build
sidecar-node-build: sidecar-build ## Build the Node.js language sidecar (depends on the base sidecar)
	docker build -f deploy/docker/sidecar-node.Dockerfile \
		--build-arg BASE_IMAGE=helmdeck-sidecar:dev \
		-t helmdeck-sidecar-node:dev .

.PHONY: sidecars
sidecars: sidecar-build sidecar-python-build sidecar-node-build ## Build every sidecar image (base + every language)

.PHONY: sidecar-smoke
sidecar-smoke: sidecar-build ## Run the sidecar headless and curl /json/version
	@CID=$$(docker run -d --rm --shm-size=2g -p 39222:9222 \
		--security-opt=no-new-privileges:true \
		helmdeck-sidecar:dev) ; \
	echo "started container $$CID" ; \
	for i in 1 2 3 4 5 6 7 8 9 10 ; do \
		if curl -fsS http://127.0.0.1:39222/json/version >/dev/null 2>&1 ; then \
			echo "CDP up after $$i tries:" ; \
			curl -fsS http://127.0.0.1:39222/json/version ; \
			docker stop $$CID >/dev/null ; exit 0 ; \
		fi ; sleep 1 ; \
	done ; \
	echo "CDP did not come up; container logs:" ; \
	docker logs $$CID ; \
	docker stop $$CID >/dev/null ; exit 1

.PHONY: compose-up
compose-up: ## docker compose up the dev stack
	docker compose -f deploy/compose/compose.yaml --env-file deploy/compose/.env up -d --build

.PHONY: compose-down
compose-down: ## docker compose down the dev stack (preserves volumes)
	docker compose -f deploy/compose/compose.yaml --env-file deploy/compose/.env down

.PHONY: compose-logs
compose-logs: ## tail control-plane logs
	docker compose -f deploy/compose/compose.yaml --env-file deploy/compose/.env logs -f control-plane

.PHONY: smoke
smoke: ## End-to-end Phase 1 exit gate: compose up -> session -> CDP -> screenshot -> tear down
	bash scripts/smoke.sh

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)

.PHONY: ci
ci: vet test build ## CI pipeline (vet + test + build)

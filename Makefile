BINARY      := bin/loot
PKG         := ./cmd/loot
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)
CONFIG      ?= configs/loot.example.yaml
NPM         ?= npm
DIST        := dist
PLATFORMS   ?= darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64
SHA256      := $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo "shasum -a 256")

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

## ---------------------------------------------------------------- build

.PHONY: deps
deps: web/node_modules ## Install frontend dependencies

web/node_modules: web/package.json
	cd web && $(NPM) install
	@touch web/node_modules

.PHONY: web
web: deps ## Build the Svelte frontend into web/dist
	cd web && $(NPM) run build

.PHONY: build
build: web ## Build the frontend, then the binary with the SPA embedded
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)
	@echo "built $(BINARY) ($(VERSION))"

.PHONY: build-go
build-go: ## Build only the Go binary (uses whatever is in web/dist)
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

## ---------------------------------------------------------------- run

.PHONY: run
run: build ## Build everything and run the server
	$(BINARY) serve --config $(CONFIG)

.PHONY: dev
dev: deps ## Run the Go server and the Vite dev server together (Ctrl-C stops both)
	@echo "api  -> http://localhost:8080"
	@echo "web  -> http://localhost:5173  (proxies /api, /ws and /hooks to :8080)"
	@trap 'kill 0' INT TERM EXIT; \
	go run $(PKG) serve --config $(CONFIG) --dev & \
	(cd web && $(NPM) run dev) & \
	wait

.PHONY: tail
tail: ## Stream drops into this terminal
	go run $(PKG) tail

## ---------------------------------------------------------------- quality

.PHONY: test
test: ## Run the Go tests
	go test ./...

.PHONY: check
check: ## Vet the Go code and type-check the frontend
	go vet ./...
	gofmt -l cmd internal web/embed.go
	cd web && $(NPM) run check

.PHONY: fmt
fmt: ## Format the Go code
	gofmt -w cmd internal web/embed.go

.PHONY: ci
ci: check test build ## Everything CI should run

## ---------------------------------------------------------------- release

.PHONY: dist
dist: web ## Cross-compile release archives for every platform into dist/
	@rm -rf $(DIST) && mkdir -p $(DIST)
	@set -e; for platform in $(PLATFORMS); do \
	  os=$${platform%/*}; arch=$${platform#*/}; \
	  name=loot_$(VERSION)_$${os}_$${arch}; \
	  stage=$(DIST)/$$name; \
	  ext=; if [ "$$os" = windows ]; then ext=.exe; fi; \
	  printf '  %-16s' "$$os/$$arch"; \
	  mkdir -p $$stage; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
	    go build -trimpath -ldflags "$(LDFLAGS)" -o $$stage/loot$$ext $(PKG); \
	  cp LICENSE README.md configs/loot.example.yaml $$stage/; \
	  if [ "$$os" = windows ]; then \
	    (cd $(DIST) && zip -qr $$name.zip $$name); \
	  else \
	    tar -czf $$stage.tar.gz -C $(DIST) $$name; \
	  fi; \
	  rm -rf $$stage; \
	  echo ok; \
	done
	@cd $(DIST) && $(SHA256) *.tar.gz *.zip > checksums.txt
	@echo
	@ls -1sh $(DIST)

.PHONY: release-snapshot
release-snapshot: dist ## Alias for `make dist` — a full release build, published nowhere

## ---------------------------------------------------------------- docker

.PHONY: docker
docker: ## Build the Docker image
	docker build -t loot:$(VERSION) -t loot:latest .

.PHONY: docker-run
docker-run: docker ## Run the Docker image with a local data volume
	docker run --rm -p 8080:8080 -v "$(PWD)/data:/data" loot:latest

## ---------------------------------------------------------------- clean

.PHONY: clean
clean: ## Remove build output (keeps your database)
	rm -rf bin web/dist $(DIST)
	@mkdir -p web/dist && touch web/dist/.gitkeep

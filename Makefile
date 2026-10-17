GO ?= go
NPM ?= npm
VERSION ?= 0.0.0-dev
LDFLAGS = -s -w -X main.version=$(VERSION)
.PHONY: deps web build build-linux build-windows build-updater build-updater-linux build-updater-windows test check gen dev dev-backend dev-web

deps:
	$(GO) mod download
	cd web && $(NPM) ci
web:
	cd web && $(NPM) run build
build: web
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/minecraft-ui ./cmd/minecraft-ui
build-linux: web
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/minecraft-ui-linux-amd64 ./cmd/minecraft-ui
build-windows: web
	mkdir -p bin
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/minecraft-ui-windows-amd64.exe ./cmd/minecraft-ui
build-updater:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/minecraft-ui-update ./cmd/minecraft-ui-update
build-updater-linux:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/minecraft-ui-update-linux-amd64 ./cmd/minecraft-ui-update
build-updater-windows:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/minecraft-ui-update-windows-amd64.exe ./cmd/minecraft-ui-update
test:
	$(GO) test ./...
check:
	$(GO) vet ./...
	$(GO) test -race ./...
	cd web && $(NPM) run typecheck && $(NPM) run lint
gen:
	cd web && $(NPM) run gen:api
dev:
	@echo 'Run make dev-backend and make dev-web in separate terminals.'
dev-backend:
	MINECRAFT_UI_ADDR=127.0.0.1:8080 MINECRAFT_UI_BASE_URL=http://127.0.0.1:5173 MINECRAFT_UI_DATA_DIR=./devdata $(GO) run ./cmd/minecraft-ui serve
dev-web:
	cd web && $(NPM) run dev

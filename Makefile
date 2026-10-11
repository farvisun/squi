BIN      := bin/squi
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
COMPOSE  := docker compose -f dev/docker-compose.yml
PREFIX   ?= $(HOME)/.local

TEST_ENV := SQUI_TEST_PG=postgres://squi:squi@127.0.0.1:54329/squi \
            SQUI_TEST_MYSQL=squi:squi@127.0.0.1:33069/squi \
            SQUI_TEST_MARIADB=squi:squi@127.0.0.1:33079/squi \
            SQUI_TEST_CLICKHOUSE=http://squi:squi@127.0.0.1:18123/squi \
            SQUI_TEST_MSSQL=sqlserver://squi:squi@127.0.0.1:14339/squi \
            SQUI_TEST_SSH=1

.PHONY: build install uninstall test test-integration vet dev dev-up dev-down dev-reset dev-sqlite snapshot clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/squi

# install copies the binary to $(PREFIX)/bin (default ~/.local/bin).
install: build
	mkdir -p $(PREFIX)/bin
	install -m 0755 $(BIN) $(PREFIX)/bin/squi
	@echo "installed $(PREFIX)/bin/squi"

uninstall:
	rm -f $(PREFIX)/bin/squi

vet:
	go vet ./...

# Unit tests; integration tests skip without the dev databases.
test:
	go test -race ./...

# Everything, against the docker playground (make dev-up first).
test-integration:
	$(TEST_ENV) go test -race -count=1 ./...

dev/keys/id_ed25519:
	mkdir -p dev/keys
	ssh-keygen -q -t ed25519 -N '' -C squi-dev -f dev/keys/id_ed25519

dev-up: dev/keys/id_ed25519
	$(COMPOSE) up -d --build

dev-down:
	$(COMPOSE) down -v

# Recreate the databases from the seed scripts.
dev-reset:
	$(COMPOSE) rm -sfv postgres mysql mariadb clickhouse sqlserver sqlserver-seed
	$(COMPOSE) up -d postgres mysql mariadb clickhouse sqlserver sqlserver-seed

# Build the SQLite playground file (no server needed).
dev-sqlite:
	go run ./dev/sqliteseed dev/seed/sqlite.sql dev/squi.db

dev/squi.db: dev/seed/sqlite.sql
	go run ./dev/sqliteseed dev/seed/sqlite.sql dev/squi.db

# Run squi against the playground with a throwaway HOME for known_hosts.
dev: build dev/squi.db
	mkdir -p .devhome
	HOME=$(CURDIR)/.devhome $(BIN) --config dev/config

snapshot:
	HOMEBREW_TAP_DEPLOY_KEY= SCOOP_BUCKET_DEPLOY_KEY= goreleaser release --snapshot --clean

clean:
	rm -rf bin dist .devhome

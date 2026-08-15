# TamizChat backend
#
# The binary is CGo-free on purpose, so `make release` cross-compiles for every
# target from any machine — no toolchain per platform, no shared libraries on
# the server.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

LDFLAGS := -s -w \
	-X tamizchat/internal/version.Version=$(VERSION) \
	-X tamizchat/internal/version.Commit=$(COMMIT)

BIN  := tamizchat
DIST := dist

.PHONY: all build run panel test race vet fmt check clean release

all: check build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/tamizchat

run: build
	./$(BIN) run

panel: build
	./$(BIN)

test:
	go test ./...

# The race detector needs CGo and a C compiler, so it is a separate target
# rather than part of `check`.
race:
	CGO_ENABLED=1 go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

check: vet test

clean:
	rm -rf $(BIN) $(BIN).exe $(DIST)

# --- development container ---
#
# The image copies in a prebuilt binary rather than compiling inside the
# container: `go mod download` needs the module proxy, which is not reachable
# from inside a container behind a proxy. Cross-compiling here costs nothing
# because the project is CGo-free.
COMPOSE_DEV := docker compose -f deploy/docker-compose.dev.yml

.PHONY: docker docker-bin dev-up dev-down dev-logs dev-panel

docker-bin:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build -trimpath -ldflags "$(LDFLAGS)" \
		-o $(DIST)/$(BIN)-linux-amd64 ./cmd/tamizchat

docker: docker-bin
	$(COMPOSE_DEV) build

dev-up: docker
	$(COMPOSE_DEV) up -d

dev-down:
	$(COMPOSE_DEV) down

dev-logs:
	$(COMPOSE_DEV) logs -f

# The interactive admin panel, against the running server's database.
dev-panel:
	$(COMPOSE_DEV) exec backend tamizchat

# Every target an operator is likely to run this on.
release: clean
	@mkdir -p $(DIST)
	@for target in linux/amd64 linux/arm64 windows/amd64 darwin/arm64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags "$(LDFLAGS)" \
			-o $(DIST)/$(BIN)-$(VERSION)-$$os-$$arch$$ext ./cmd/tamizchat || exit 1; \
	done
	@echo
	@ls -lh $(DIST)

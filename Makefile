# shelve — Docker-driven build system (master plan §7, phase E2).
#
# The host needs Docker ONLY. All compilation happens inside the shelve-dev
# image (golang:1.25-trixie + Node/npm + Electron/Chromium runtime libs +
# electron-builder). The `run` target is the only one that executes code on the
# host: it needs the Electron/Chromium runtime libraries from the host distro
# (documented in E5).

APP     := shelve
IMAGE   := shelve-dev
ROOT    := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
CONFIG  := $(HOME)/.config/$(APP)
# Single source for the dev renderer port: Vite is launched with it and the
# Electron shell is pointed at it (main.ts validates it is loopback).
DEV_PORT := 5173
UID     := $(shell id -u)
GID     := $(shell id -g)

# Version source of truth: the root package.json `version` (E2-D6; keep
# internal/app.Version in sync). The host is Docker-only, so fall back to a
# plain text parse when node is unavailable.
APP_VERSION := $(shell node -p "require('./package.json').version" 2>/dev/null || sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' package.json 2>/dev/null | head -n1)
ifeq ($(strip $(APP_VERSION)),)
APP_VERSION := 0.0.0
endif

# Persistent caches so repeated container runs don't re-download the Go
# modules, npm packages, or the Electron / electron-builder artifacts.
CACHE_FLAGS := -v shelve-dev-gomod:/go/pkg/mod \
               -v shelve-dev-npm:/root/.npm \
               -v shelve-dev-electron:/root/.cache/electron \
               -v shelve-dev-electron-builder:/root/.cache/electron-builder

# All in-container work mounts the repo at /app (the image's workdir).
DOCKER_RUN := docker run --rm --init $(CACHE_FLAGS) -v $(ROOT):/app -w /app

# X11 forwarding for the interactive in-container dev session. Works on X11 and
# on Wayland via XWayland (see README). Uses $XAUTHORITY or ~/.Xauthority.
XAUTH_FILE  := $(firstword $(wildcard $(XAUTHORITY) $(HOME)/.Xauthority))
XAUTH_FLAGS := $(if $(XAUTH_FILE),-v $(XAUTH_FILE):/root/.Xauthority -e XAUTHORITY=/root/.Xauthority)
# /dev/dri (when present) gives Chromium a real GPU; without it the renderer
# falls back to software rasterization.
DRI_FLAGS   := $(if $(wildcard /dev/dri),-v /dev/dri:/dev/dri)
# NO_AT_BRIDGE: the X server advertises the host user's at-spi bus (X root
# property AT_SPI_BUS); the socket is not mounted, and GTK abort-traps trying to
# use it. Accessibility is not needed for development in the container.
A11Y_FLAGS  := -e NO_AT_BRIDGE=1
X11_FLAGS   := -v /tmp/.X11-unix:/tmp/.X11-unix -e DISPLAY=$(DISPLAY) $(DRI_FLAGS) $(A11Y_FLAGS) $(XAUTH_FLAGS)

# Container runs as root, so return repo-owned artifacts to the invoking user.
# chrome-sandbox is excluded from the recursive chown: Chromium needs it
# root:root mode 4755, and `chown` clears setuid. Re-assert it after.
fix-owner:
	docker run --rm --init -v $(ROOT):/app --entrypoint sh $(IMAGE) -c '\
		chown -R $(UID):$(GID) /app/bin /app/frontend/dist /app/dist-electron /app/node_modules 2>/dev/null || true; \
		sandbox=/app/bin/linux-unpacked/chrome-sandbox; \
		if [ -e "$$sandbox" ]; then chown root:root "$$sandbox" && chmod 4755 "$$sandbox"; fi'

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-z][a-z-]*:.*## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN{FS=":.*## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------- image ---

.PHONY: dev-image
dev-image: ## Build the shelve-dev toolchain image
	docker build -t $(IMAGE) -f Dockerfile.dev .

.PHONY: ensure-image
ensure-image:
	@docker image inspect $(IMAGE) >/dev/null 2>&1 || $(MAKE) --no-print-directory dev-image

# ------------------------------------------------------------- js deps ---
# The single root package.json + lockfile own the whole JS build (E2-D1).
# `npm ci` installs into the host-mounted /app/node_modules.

.PHONY: node-deps
node-deps: ensure-image
	$(DOCKER_RUN) $(IMAGE) sh -c '[ -d node_modules ] || npm ci --no-audit --no-fund'

# ------------------------------------------------------------------ dev ---

.PHONY: dev
dev: ensure-image node-deps ## Hot-reload dev session in the container (X11); Electron loads the Vite dev server
	@install -d -m 0700 $(CONFIG)
	# Build the backend first: main.ts spawns bin/shelve-backend in dev, and bin/
	# is gitignored/cleaned. Vite runs in the background, Electron in the
	# foreground; the trap stops Vite when Electron exits. `--network host` lets
	# Electron reach the Vite port on 127.0.0.1; Vite is pinned to $(DEV_PORT)
	# so the URL below is the single dev endpoint. `--no-sandbox` is the
	# documented in-container root fallback (master plan §8.11);
	# `make build && make run` is the always-supported loop.
	$(DOCKER_RUN) $(X11_FLAGS) --network host $(IMAGE) sh -c '\
		set -e; \
		go build -o bin/shelve-backend ./cmd/shelve-backend; \
		npm run dev:renderer -- --port $(DEV_PORT) & vite_pid=$$!; \
		trap "kill $$vite_pid 2>/dev/null || true" EXIT INT TERM; \
		npm run build:electron; \
		SHELVE_DEV_URL=http://127.0.0.1:$(DEV_PORT) npx electron . --no-sandbox'
	$(MAKE) fix-owner

# ----------------------------------------------------------------- build ---

# Shared payload pipeline for `build` and `appimage`: keeping it in one place
# means the shipped AppImage cannot drift from the `--linux dir` build that
# `make run` exercises. The two targets differ only in the electron-builder
# target they invoke.
.PHONY: payload
payload: ensure-image ## Build the Go backend + renderer + Electron bundles (shared by build/appimage)
	$(DOCKER_RUN) $(IMAGE) sh -c '\
		set -e; \
		go build -o bin/shelve-backend ./cmd/shelve-backend; \
		npm ci --no-audit --no-fund; \
		npm run build:renderer; \
		npm run build:electron'
	$(MAKE) fix-owner

.PHONY: build
build: payload ## Release build in the container -> bin/linux-unpacked/shelve
	$(DOCKER_RUN) $(IMAGE) npx electron-builder --linux dir
	$(MAKE) fix-owner

.PHONY: run
run: ## Run the packaged app on the host (needs the Electron/Chromium runtime libs)
	@test -x bin/linux-unpacked/$(APP) || { echo "bin/linux-unpacked/$(APP) not found - run 'make build' first" >&2; exit 1; }
	@if ldd ./bin/linux-unpacked/$(APP) 2>/dev/null | grep -q "not found"; then \
		echo "missing Electron/Chromium runtime libraries:" >&2; \
		ldd ./bin/linux-unpacked/$(APP) | grep "not found" >&2; \
		echo "install the host runtime libs listed in README.md (Prerequisites), e.g. on Debian/Ubuntu/ALT:" >&2; \
		echo "  $$(grep -v '^#' $(ROOT)/scripts/host-runtime-libs.txt | tr '\n' ' ')" >&2; \
		exit 1; \
	fi
	./bin/linux-unpacked/$(APP)

# ------------------------------------------------------------- appimage ---

.PHONY: appimage
appimage: payload ## Build the self-contained AppImage in the container -> bin/shelve-<version>-x86_64.AppImage
	$(DOCKER_RUN) $(IMAGE) npx electron-builder --linux AppImage
	$(MAKE) fix-owner
	@echo "AppImage ready: $$(ls -1 bin/shelve-*.AppImage 2>/dev/null | head -1)"

.PHONY: appimage-check
appimage-check: ## Verify the AppImage payload + dependency self-containment (scripts/verify-appimage.sh)
	./scripts/verify-appimage.sh

.PHONY: package
package: appimage ## Alias of appimage

.PHONY: clean
clean: ## Remove build outputs
	rm -rf bin frontend/dist dist-electron node_modules

# ----------------------------------------------------------------- test ---

.PHONY: test
test: ensure-image ## Go unit tests in the container
	$(DOCKER_RUN) $(IMAGE) go test ./...

.PHONY: test-race
test-race: ensure-image ## Go unit tests with the race detector
	$(DOCKER_RUN) $(IMAGE) go test -race ./...

.PHONY: test-integration
test-integration: ensure-image ## SFTP/SSH integration tests (testcontainers; needs Docker socket)
	@if [ ! -S /var/run/docker.sock ]; then echo "Docker socket /var/run/docker.sock not found" >&2; exit 1; fi
	# --network host lets the test (running inside the shelve-dev container) reach
	# the sshd container's published port on the host loopback (testcontainers).
	$(DOCKER_RUN) -v /var/run/docker.sock:/var/run/docker.sock --network host $(IMAGE) go test -tags integration ./internal/sftp/

.PHONY: smoke-vault
smoke-vault: ensure-image ## Headless vault smoke (create→unlock→modify→lock) against a temp dir
	$(DOCKER_RUN) $(IMAGE) go run ./internal/vault/smol

# -------------------------------------------------------------- seed tool ---
# Seed/unseed build the tiny pure-Go QA tool in the container (consistent
# with the docker-driven workflow), then RUN the binary on the host so it
# writes into the HOST config dir — exactly where `make run` reads it.

SEED_BIN := bin/seed

.PHONY: seed
seed: ensure-image ## Seed a 300-session QA vault into the host config dir
	$(DOCKER_RUN) $(IMAGE) go build -o $(SEED_BIN) ./cmd/seed
	$(MAKE) fix-owner
	./$(SEED_BIN)

.PHONY: unseed
unseed: ensure-image ## Remove the seeded vault.json + known_hosts (keeps settings)
	$(DOCKER_RUN) $(IMAGE) go build -o $(SEED_BIN) ./cmd/seed
	$(MAKE) fix-owner
	./$(SEED_BIN) -unseed

# ----------------------------------------------------------------- lint ---
# Note: build/ is excluded from gofmt — it holds historical platform assets
# that are not gofmt-clean upstream (removed in E6).

.PHONY: lint
lint: ensure-image node-deps ## gofmt + go vet (container) + electron & renderer tsc --noEmit
	$(DOCKER_RUN) $(IMAGE) sh -c '\
		set -e; \
		cd /app; \
		unformatted=$$(gofmt -l . | grep -v "^build/" || true); \
		if [ -n "$$unformatted" ]; then echo "gofmt: files need formatting:"; echo "$$unformatted"; exit 1; fi; \
		go vet ./...; \
		npm run typecheck'

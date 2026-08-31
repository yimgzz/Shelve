# shelve — Docker-driven build system (master plan §7).
#
# The host needs Docker ONLY. All compilation happens inside the shelve-dev
# image (golang:1.25-trixie + GTK4/WebKitGTK 6.0 + Node + pinned wails3 CLI).
# The `run` target is the only one that executes code on the host: it needs
# the GTK4 + WebKitGTK 6.0 runtime libraries from the host distro.

APP     := shelve
IMAGE   := shelve-dev
VITE_PORT := 9245
ROOT    := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
CONFIG  := $(HOME)/.config/$(APP)
UID     := $(shell id -u)
GID     := $(shell id -g)

# AppImage filename version — installers require <name>-<version>.AppImage
# (appimagetool: "FATAL: Can't get version from ..."). Single source of
# truth is build/config.yml `info.version`.
APP_VERSION := $(shell awk -F'"' '/^[[:space:]]*version:[[:space:]]*"/{print $$2; exit}' build/config.yml)
ifeq ($(strip $(APP_VERSION)),)
APP_VERSION := 0.0.0
endif
# Host arch in wails/linuxdeploy naming (x86_64 / aarch64).
PACKAGE_ARCH := $(shell uname -m)

# Persistent caches so repeated container runs don't re-download
# Go modules / npm packages.
CACHE_FLAGS := -v shelve-dev-gomod:/go/pkg/mod -v shelve-dev-npm:/root/.npm-cache

# All in-container work mounts the repo at /app (the image's workdir).
DOCKER_RUN := docker run --rm --init $(CACHE_FLAGS) -v $(ROOT):/app -w /app

# X11 forwarding for interactive in-container runs. Works on X11 and on
# Wayland via XWayland (see README). Uses $XAUTHORITY (e.g. mutter's
# XWayland auth file) or falls back to ~/.Xauthority.
XAUTH_FILE  := $(firstword $(wildcard $(XAUTHORITY) $(HOME)/.Xauthority))
XAUTH_FLAGS := $(if $(XAUTH_FILE),-v $(XAUTH_FILE):/root/.Xauthority -e XAUTHORITY=/root/.Xauthority)
# GDK_BACKEND=x11: the container always speaks X11 (XWayland on the host
# desktop); never try to reach a Wayland socket that is not mounted.
# /dev/dri (when present) gives WebKitGTK a real GPU for the dmabuf
# renderer; without it the web view hangs creating its EGL screen.
# GTK_A11Y=none + NO_AT_BRIDGE: the X server advertises the host user's
# at-spi bus (X root-window property AT_SPI_BUS); the socket is not
# mounted into the container, and GTK fatal-aborts (SIGTRAP) trying to
# use it. Accessibility is not needed for development in the container.
DRI_FLAGS   := $(if $(wildcard /dev/dri),-v /dev/dri:/dev/dri)
# NO_AT_BRIDGE/GTK_A11Y=none: never reach for the at-spi bus (see above).
A11Y_FLAGS  := -e NO_AT_BRIDGE=1 -e GTK_A11Y=none
X11_FLAGS   := -v /tmp/.X11-unix:/tmp/.X11-unix -e DISPLAY=$(DISPLAY) -e GDK_BACKEND=x11 $(DRI_FLAGS) $(A11Y_FLAGS) $(XAUTH_FLAGS)

# Container runs as root, so return repo-owned artifacts to the invoking
# user (chown metadata only; mirrors the wails build:docker pattern).
# build/linux/appimage/build is the AppImage scratch dir (gitignored; P004).
fix-owner:
	docker run --rm --init -v $(ROOT):/app --entrypoint chown $(IMAGE) -R $(UID):$(GID) /app/bin /app/frontend/dist /app/frontend/bindings /app/frontend/node_modules /app/build/linux/appimage/build 2>/dev/null || true

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

# ------------------------------------------------------------------ dev ---

.PHONY: wails-init
wails-init: ensure-image ## Re-run `wails3 init` in the container and merge the template into the repo
	$(DOCKER_RUN) --entrypoint sh $(IMAGE) -c '\
		set -e; \
		rm -rf /tmp/dsm-init; mkdir -p /tmp/dsm-init; cd /tmp/dsm-init; \
		wails3 init -n $(APP) -t vanilla; \
		tar -C $(APP) --exclude=.gitignore --exclude=README.md -cf - . | tar -C /app -xf -'
	$(MAKE) fix-owner

.PHONY: dev
dev: ensure-image ## Hot-reload dev session in the container (X11 + --network host); the window opens on the desktop
	@install -d -m 0700 $(CONFIG)
	$(DOCKER_RUN) $(X11_FLAGS) --network host $(IMAGE) dev -config ./build/config.yml -port $(VITE_PORT)	
	$(MAKE) fix-owner

# ----------------------------------------------------------------- build ---

.PHONY: build
build: ensure-image ## Release build in the container -> bin/$(APP)
	$(DOCKER_RUN) $(IMAGE) build
	$(MAKE) fix-owner

.PHONY: run
run: ## Run the built binary on the host (needs host GTK4 + WebKitGTK 6.0)
	@test -x bin/$(APP) || { echo "bin/$(APP) not found - run 'make build' first" >&2; exit 1; }
	./bin/$(APP)

.PHONY: clean
clean: ## Remove build outputs
	rm -rf bin frontend/dist frontend/bindings

# ----------------------------------------------------------------- test ---

.PHONY: test
test: ensure-image ## Go unit tests in the container
	$(DOCKER_RUN) --entrypoint go $(IMAGE) test ./...

.PHONY: test-race
test-race: ensure-image ## Go unit tests with the race detector
	$(DOCKER_RUN) --entrypoint go $(IMAGE) test -race ./...

.PHONY: test-integration
test-integration: ensure-image ## SFTP/SSH integration tests (testcontainers; needs Docker socket)
	@if [ ! -S /var/run/docker.sock ]; then echo "Docker socket /var/run/docker.sock not found" >&2; exit 1; fi
	# --network host lets the test (running inside the shelve-dev container) reach
	# the sshd container's published port on the host loopback (testcontainers).
	$(DOCKER_RUN) -v /var/run/docker.sock:/var/run/docker.sock --network host --entrypoint go $(IMAGE) test -tags integration ./internal/sftp/

.PHONY: smoke-vault
smoke-vault: ensure-image ## Headless vault smoke (create→unlock→modify→lock) against a temp dir
	$(DOCKER_RUN) --entrypoint go $(IMAGE) run ./internal/vault/smol

# -------------------------------------------------------------- seed tool ---
# Seed/unseed build the tiny pure-Go QA tool in the container (consistent
# with the docker-driven workflow), then RUN the binary on the host so it
# writes into the HOST config dir — exactly where `make run` reads it.

SEED_BIN := bin/seed

.PHONY: seed
seed: ensure-image ## Seed a 300-session QA vault into the host config dir
	$(DOCKER_RUN) --entrypoint go $(IMAGE) build -o $(SEED_BIN) ./cmd/seed
	$(MAKE) fix-owner
	./$(SEED_BIN)

.PHONY: unseed
unseed: ensure-image ## Remove the seeded vault.json + known_hosts (keeps settings)
	$(DOCKER_RUN) --entrypoint go $(IMAGE) build -o $(SEED_BIN) ./cmd/seed
	$(MAKE) fix-owner
	./$(SEED_BIN) -unseed

# ----------------------------------------------------------------- lint ---
# Note: build/ is excluded from gofmt — it holds wails-generated platform
# assets that are not gofmt-clean upstream.

.PHONY: lint
lint: ensure-image ## gofmt + go vet (container) + frontend tsc --noEmit
	$(DOCKER_RUN) --entrypoint sh $(IMAGE) -c '\
		set -e; \
		cd /app; \
		unformatted=$$(gofmt -l . | grep -v "^build/" || true); \
		if [ -n "$$unformatted" ]; then echo "gofmt: files need formatting:"; echo "$$unformatted"; exit 1; fi; \
		go vet ./...; \
		cd frontend; \
		[ -d node_modules ] || npm install --no-audit --no-fund; \
		npx tsc --noEmit'

# ------------------------------------------------------------ packaging ---
# AppImage packaging (plan P004). AppImage is the only wired format; DEB/RPM
# stay stubs per the cancelled Phase 6 (master §10). Everything runs in the
# shelve-dev container: `wails3 task linux:create:appimage` = release build +
# .desktop generation + `wails3 generate appimage` (linuxdeploy + GTK4 /
# WebKitGTK bundling, master §7/§11).

.PHONY: package
package: appimage ## Package the AppImage (single format; DEB/RPM remain stubs per cancelled Phase 6)

.PHONY: appimage
appimage: ensure-image ## Build a self-contained AppImage -> bin/shelve-<version>-<arch>.AppImage
	$(DOCKER_RUN) $(IMAGE) task linux:create:appimage
	$(MAKE) fix-owner
	cp bin/$(APP)-$(PACKAGE_ARCH).AppImage bin/$(APP)-$(APP_VERSION)-$(PACKAGE_ARCH).AppImage

.PHONY: appimage-check
appimage-check: ## Verify bin/shelve-<arch>.AppImage contents + zero-dep ldd check (plan P004 T3)
	./scripts/verify-appimage.sh

# AppImage built on ALT Linux p11 — the project's primary distro family.
# The trixie-built AppImage above requires host glibc >= 2.39 and libstdc++
# with CXXABI_1.3.15 (GCC 14). alt:p11 (glibc 2.38 + GCC 13.2.1 + WebKitGTK
# 6.0) lowers the portability floor to glibc >= 2.38 / GCC 13 libstdc++ —
# ALT p10/p11, Ubuntu 24.04+, Fedora 39+, Debian 13, Arch (master §11).
APPIMAGE_ALT_IMAGE := shelve-dev-appimage-alt

.PHONY: appimage-alt-image
appimage-alt-image: ## Build the ALT p11 AppImage toolchain image (Dockerfile.appimage-alt)
	docker build -t $(APPIMAGE_ALT_IMAGE) -f Dockerfile.appimage-alt .

.PHONY: ensure-appimage-alt-image
ensure-appimage-alt-image:
	@docker image inspect $(APPIMAGE_ALT_IMAGE) >/dev/null 2>&1 || $(MAKE) --no-print-directory appimage-alt-image

.PHONY: appimage-alt
appimage-alt: ensure-appimage-alt-image ## Build a portable AppImage on ALT p11 (glibc 2.38 floor) -> bin/shelve-<version>-<arch>.AppImage
	docker run --rm --init \
		-v shelve-dev-gomod:/go/pkg/mod \
		-v shelve-dev-npm:/root/.npm \
		-v $(ROOT):/app -w /app \
		$(APPIMAGE_ALT_IMAGE) task linux:create:appimage
	$(MAKE) fix-owner
	cp bin/$(APP)-$(PACKAGE_ARCH).AppImage bin/$(APP)-$(APP_VERSION)-$(PACKAGE_ARCH).AppImage

.PHONY: deb
deb: ensure-image ## Build a single DEB
	@echo "not implemented (cancelled Phase 6; nfpm config present but unwired)"

.PHONY: rpm
rpm: ensure-image ## Build a single RPM
	@echo "not implemented (cancelled Phase 6; nfpm config present but unwired)"

.PHONY: build-win
build-win: ensure-image ## Cross-build the Windows binary (zig image, unsigned)
	@echo "not implemented yet (Phase 6: wails-cross cross image)"

.PHONY: build-darwin
build-darwin: ensure-image ## Cross-build the macOS binary (zig image, unsigned)
	@echo "not implemented yet (Phase 6: wails-cross cross image)"

.PHONY: flatpak
flatpak: ensure-image ## Stretch goal: flatpak-builder pipeline
	@echo "not implemented yet (Phase 6 stretch: dedicated flatpak image)"

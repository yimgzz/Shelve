# dummy-ssh-manager — Docker-driven build system (master plan §7).
#
# The host needs Docker ONLY. All compilation happens inside the dsm-dev
# image (golang:1.25-trixie + GTK4/WebKitGTK 6.0 + Node + pinned wails3 CLI).
# The `run` target is the only one that executes code on the host: it needs
# the GTK4 + WebKitGTK 6.0 runtime libraries from the host distro.

APP     := dummy-ssh-manager
IMAGE   := dsm-dev
VITE_PORT := 9245
ROOT    := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
CONFIG  := $(HOME)/.config/$(APP)
UID     := $(shell id -u)
GID     := $(shell id -g)

# Persistent caches so repeated container runs don't re-download
# Go modules / npm packages.
CACHE_FLAGS := -v dsm-dev-gomod:/go/pkg/mod -v dsm-dev-npm:/root/.npm-cache

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
fix-owner:
	docker run --rm --init -v $(ROOT):/app --entrypoint chown $(IMAGE) -R $(UID):$(GID) /app/bin /app/frontend/dist /app/frontend/bindings /app/frontend/node_modules 2>/dev/null || true

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-z][a-z-]*:.*## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN{FS=":.*## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------- image ---

.PHONY: dev-image
dev-image: ## Build the dsm-dev toolchain image
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
test-integration: ensure-image ## SSH integration tests (testcontainers)
	@echo "not implemented yet (Phase 3: docker/sshd testcontainers setup)"

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

.PHONY: package
package: ensure-image ## Package AppImage + DEB + RPM
	@echo "not implemented yet (Phase 6: wails3 package GOOS=linux -> AppImage/DEB/RPM)"

.PHONY: appimage
appimage: ensure-image ## Build a single AppImage
	@echo "not implemented yet (Phase 6)"

.PHONY: deb
deb: ensure-image ## Build a single DEB
	@echo "not implemented yet (Phase 6)"

.PHONY: rpm
rpm: ensure-image ## Build a single RPM
	@echo "not implemented yet (Phase 6)"

.PHONY: build-win
build-win: ensure-image ## Cross-build the Windows binary (zig image, unsigned)
	@echo "not implemented yet (Phase 6: wails-cross cross image)"

.PHONY: build-darwin
build-darwin: ensure-image ## Cross-build the macOS binary (zig image, unsigned)
	@echo "not implemented yet (Phase 6: wails-cross cross image)"

.PHONY: flatpak
flatpak: ensure-image ## Stretch goal: flatpak-builder pipeline
	@echo "not implemented yet (Phase 6 stretch: dedicated flatpak image)"

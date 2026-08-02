GO ?= go
DIST := dist
BIN := $(DIST)/shutdowner.exe
LOGO := logo.png
ICON := assets/icon.ico
FAVICON := internal/web/static/favicon.ico
# Naming a .syso for an explicit GOOS_GOARCH is what keeps the Go linker from
# feeding an amd64 COFF object to a build for any other target.
SYSO := cmd/shutdowner/rsrc_windows_amd64.syso
RSRC := github.com/akavel/rsrc@v0.10.2

# Recipes run under PowerShell on Windows and /bin/sh elsewhere. Shell-specific
# fragments (env prefixes, mkdir -p, rm -rf) are factored into variables so the
# recipes below stay identical on both.
ifeq ($(OS),Windows_NT)
SHELL := powershell.exe
.SHELLFLAGS := -NoProfile -NoLogo -Command
WIN_ENV := $$env:GOOS='windows'; $$env:GOARCH='amd64';
BUILD_ENV := $(WIN_ENV) $$env:CGO_ENABLED='0';
MKDIR_DIST := New-Item -ItemType Directory -Force -Path $(DIST) | Out-Null
RM_DIST := if (Test-Path $(DIST)) { Remove-Item -Recurse -Force $(DIST) }
else
WIN_ENV := GOOS=windows GOARCH=amd64
BUILD_ENV := $(WIN_ENV) CGO_ENABLED=0
MKDIR_DIST := mkdir -p $(DIST)
RM_DIST := rm -rf $(DIST)
endif

.PHONY: test vet build-windows run-dev fmt icon clean

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...
	$(WIN_ENV) $(GO) vet ./...

fmt:
	$(GO) fmt ./...

build-windows:
	$(MKDIR_DIST)
	$(BUILD_ENV) $(GO) build -ldflags "-s -w" -o $(BIN) ./cmd/shutdowner

run-dev:
	$(GO) run ./cmd/shutdowner --console --fake-power --config ./.env.dev

# Rebuilds the executable's icon and the web UI's favicon from $(LOGO). Every
# output is committed, the Go toolchain links the .syso in on its own and
# go:embed picks the favicon up, so build-windows does not depend on this: it
# only needs running when the logo changes, and it is the only target that
# reaches the network (for rsrc, on a cold module cache).
#
# The favicon carries only the sizes a browser has a use for, and stores them as
# PNG rather than as the DIBs the Windows shell prefers, which takes it from
# 15 KB to 4 KB.
icon:
	$(GO) run ./tools/mkicon -in $(LOGO) -out $(ICON)
	$(GO) run ./tools/mkicon -in $(LOGO) -out $(FAVICON) -sizes 16,32,48 -dib-max 0
	$(GO) run $(RSRC) -ico $(ICON) -arch amd64 -o $(SYSO)

clean:
	$(RM_DIST)

GO ?= go
DIST := dist
BIN := $(DIST)/shutdowner.exe

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

.PHONY: test vet build-windows run-dev fmt clean

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

clean:
	$(RM_DIST)

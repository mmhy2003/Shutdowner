GO ?= go
BIN := dist/shutdowner.exe

.PHONY: test vet build-windows run-dev fmt clean

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...
	GOOS=windows GOARCH=amd64 $(GO) vet ./...

fmt:
	$(GO) fmt ./...

build-windows:
	mkdir -p dist
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build -ldflags "-s -w" -o $(BIN) ./cmd/shutdowner

run-dev:
	$(GO) run ./cmd/shutdowner --console --fake-power --config ./.env.dev

clean:
	rm -rf dist

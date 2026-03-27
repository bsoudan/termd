.PHONY: all build-server build-tui build-tui-windows build-termctl check-windows test test-e2e clean

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -X main.version=$(VERSION)
ifndef RELEASE
  GCFLAGS := -gcflags "all=-N -l"
endif

all: build-server build-tui build-tui-windows build-termctl

build-server:
	cd server && go build $(GCFLAGS) -ldflags "$(LDFLAGS)" -o ../.local/bin/termd .

build-tui:
	cd frontend && go build $(GCFLAGS) -ldflags "$(LDFLAGS)" -o ../.local/bin/termd-tui .

build-tui-windows:
	cd frontend && GOOS=windows GOARCH=amd64 go build $(GCFLAGS) -ldflags "$(LDFLAGS)" -o ../.local/bin/termd-tui.exe .

build-termctl:
	cd termctl && go build $(GCFLAGS) -ldflags "$(LDFLAGS)" -o ../.local/bin/termctl .

check-windows:
	cd frontend && GOOS=windows GOARCH=amd64 go build -o /dev/null .
	cd transport && GOOS=windows GOARCH=amd64 go build -o /dev/null .

test: test-e2e

test-e2e: all
	cd e2e && PATH="$(CURDIR)/.local/bin:$(PATH)" go test -v -timeout 120s

clean:
	rm -rf .local/bin
	cd server && go clean ./...
	cd frontend && go clean ./...
	cd termctl && go clean ./...

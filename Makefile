.PHONY: all build-server changelog build-tui build-tui-windows build-termctl build-mousehelper check-windows test test-e2e test-stress test-stress-long clean

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/-g[0-9a-f]*//;s/-dirty/*/' || echo "dev")
LDFLAGS := -X main.version=$(VERSION)
ifndef RELEASE
  GCFLAGS := -gcflags "all=-N -l"
endif

all: build-server build-tui build-tui-windows build-termctl build-mousehelper

build-server:
	go build $(GCFLAGS) -ldflags "$(LDFLAGS)" -o .local/bin/termd ./server

changelog:
	@tmp=$$(mktemp); \
	if ! git diff --quiet HEAD 2>/dev/null || test -n "$$(git ls-files --others --exclude-standard)"; then \
		printf '%18s %s\n' "$$(git describe --tags --always --dirty='*' 2>/dev/null):" "$$(git status --short | tr '\n' ' ')" > "$$tmp"; \
	fi; \
	git log --format='%H %s' -100 | while read hash rest; do \
		ver=$$(git describe --tags --always $$hash 2>/dev/null); \
		printf '%18s %s\n' "$$ver:" "$$rest"; \
	done >> "$$tmp"; \
	mv "$$tmp" frontend/changelog.txt

build-tui: changelog
	go build $(GCFLAGS) -ldflags "$(LDFLAGS)" -o .local/bin/ttui ./frontend
	ln -sf ttui .local/bin/termd-tui

build-tui-windows: changelog
	GOOS=windows GOARCH=amd64 go build $(GCFLAGS) -ldflags "$(LDFLAGS)" -o .local/bin/ttui.exe ./frontend

build-mousehelper:
	cd e2e/testdata/mousehelper && go build -o ../../../.local/bin/mousehelper .

build-termctl:
	go build $(GCFLAGS) -ldflags "$(LDFLAGS)" -o .local/bin/termctl ./termctl

check-windows:
	GOOS=windows GOARCH=amd64 go build -o /dev/null ./frontend
	GOOS=windows GOARCH=amd64 go build -o /dev/null ./transport

test: test-e2e

test-e2e: all
	PATH="$(CURDIR)/.local/bin:$(PATH)" go test -v -timeout 120s ./e2e

# Stress test (quick). Override with env vars:
#   STRESS_TUI_CLIENTS  — number of termd-tui instances    (default: 5)
#   STRESS_RAW_CLIENTS  — number of raw protocol clients   (default: 3)
#   STRESS_DURATION     — how long to run                  (default: 30s)
#   STRESS_SEED         — fixed RNG seed for reproduction  (default: random)
test-stress: all
	PATH="$(CURDIR)/.local/bin:$(PATH)" go test -v -tags stress -run TestStress -timeout 300s ./e2e

test-stress-long: all
	PATH="$(CURDIR)/.local/bin:$(PATH)" \
		STRESS_TUI_CLIENTS=10 STRESS_RAW_CLIENTS=5 STRESS_DURATION=120s \
		go test -v -tags stress -run TestStress -timeout 300s ./e2e

clean:
	rm -rf .local/bin
	go clean ./...

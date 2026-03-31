# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test

Always use `make` to build. Do not run `go build` directly.

```bash
make                    # build everything (server, tui, termctl, mousehelper, upgrade binaries)
make build-server       # build server only → .local/bin/termd
make build-tui          # build TUI client only → .local/bin/ttui
make build-termctl      # build termctl CLI → .local/bin/termctl
make test               # run e2e tests (builds everything first)
make test-stress        # quick stress test (30s default)
make test-stress-long   # extended stress test (120s, more clients)
make check-windows      # cross-compile check for Windows
```

Run a single test:
```bash
go test -v -timeout 120s -run TestName ./e2e
```

Run pkg/te unit tests:
```bash
go test -v ./pkg/te
```

Set `RELEASE=1` to build optimized binaries (no debug symbols).

## Architecture

**termd** is a terminal multiplexer with a client-server architecture. The server manages PTYs and terminal state; clients connect over the network to view and interact with terminals.

### Server (`server/`)

The server uses a **single event loop** for all mutable state (regions, clients, sessions). No mutexes protect server-level maps — all mutations go through `Server.requests` channel and are handled in `eventLoop()` (`server_requests.go`). Client and region goroutines communicate with the event loop via typed request structs with response channels.

- **Region** (`region.go`): wraps a PTY + child process + VT parser (`pkg/te`). A `readLoop` goroutine reads PTY output, feeds it through an `EventProxy`, and notifies subscribed clients. The Region holds a `sync.Mutex` only for its own screen state (snapshot reads).
- **Client** (`client.go`): wraps a `net.Conn`. Has a dedicated `writeLoop` goroutine that drains a `writeCh` channel, with backpressure detection (drops data and sends warnings if the client falls behind). A `readLoop` goroutine reads JSON messages and dispatches them to the server event loop.
- **Session** (`session.go`): groups regions. A session is created on first `session_connect_request` and spawns configured default programs.
- **EventProxy** (`event_proxy.go`): sits between the VT parser and the client write path. Captures terminal events into a batch, handles synchronized output mode (mode 2026) by buffering events until sync completes, then sending a full snapshot instead.
- **Live upgrade** (`upgrade.go`, `upgrade_protocol.go`, `upgrade_recv.go`): supports zero-downtime server replacement. The old server serializes all state (regions, clients, sessions) to the new process over a Unix socket, including PTY file descriptors.

### Frontend / TUI (`frontend/`)

Built on **bubbletea v2** with **lipgloss v2** for rendering.

- **Model** (`ui/model.go`): top-level bubbletea model that owns a **layer stack**. Messages propagate top-down through layers; the first layer that handles a message stops propagation.
- **Layer interface** (`ui/layer.go`): all UI components implement `Layer` (Update/View/Activate/Deactivate). Layers return `[]*lipgloss.Layer` slices that get composited together.
- **MainLayer** (`ui/mainlayer.go`): the base layer — manages the tab bar, terminal viewport, and region subscriptions.
- **Terminal** (`ui/terminal.go`): local VT screen that replays `terminal_events` from the server to maintain a synchronized copy of the remote terminal.
- **Raw input** (`ui/rawio.go`): after bubbletea Init completes, stdin is read directly and forwarded as `RawInputMsg` to avoid bubbletea's input parsing. This preserves exact terminal escape sequences for the remote PTY.
- **Server connection** (`ui/server.go`): manages the WebSocket/Unix/TCP connection to the server, with automatic reconnection (exponential backoff, 100ms–60s).
- **Overlay layers**: `connectlayer.go` (session picker), `commandlayer.go` (command palette), `helplayer.go`, `programlayer.go` (spawn program), `scrollablelayer.go` (scrollback), `statuslayer.go`.
- **Tasks** (`ui/task.go`): synchronous goroutine abstraction over bubbletea's async event loop. A task is a goroutine that communicates with bubbletea through a channel bridge managed by `TaskRunner` in Model. Tasks call `Request()` to make server roundtrips, `WaitFor(filter)` to wait for messages, and `PushLayer()`/`PopLayer()` to manage overlays — all as blocking calls. The `WaitFor` filter returns `(deliver, handled bool)` mirroring the layer pattern: `deliver` routes the message to the task, `handled` controls whether layers also see it. Use tasks for multi-step async workflows (e.g. upgrade); simple single-step overlays (help, picker, input) should stay as plain layers.

### Protocol (`frontend/protocol/`, `protocol.md`)

Newline-delimited JSON over any transport. Request/response pattern: every request has a corresponding response with `error` bool + `message` string. Exceptions: `identify` and `input` are fire-and-forget. Server-initiated messages (`screen_update`, `terminal_events`, `region_created`, `region_destroyed`) have no response.

The protocol is documented in `protocol.md`.

### Transport (`transport/`)

Transport-agnostic `Listen(spec)` / `Dial(spec)` functions. Supported schemes: `unix:`, `tcp:`, `ws:`/`wss:`, `ssh:`. All return `net.Listener`/`net.Conn` — the rest of the codebase is transport-unaware.

### Terminal Emulator (`pkg/te/`)

A full VT100/xterm terminal emulator library. `Stream` parses escape sequences, `Screen` maintains cell state with colors/attributes, `HistoryScreen` wraps Screen with scrollback. Extensively tested with esctest2 test suite ports and pyte compatibility tests.

### termctl (`termctl/`)

CLI admin tool for the server. Connects using `frontend/client` and sends protocol messages (status, list regions, spawn, kill, etc.).

### Config (`config/`)

TOML configuration. Server: `~/.config/termd/server.toml`. Frontend: `~/.config/termd-tui/config.toml`.

## Coding

* Never build code in tests — put build commands in the Makefile and use that.

* When writing tests, never use sleep unless absolutely necessary. Prefer explicit signaling/checking for readiness.

* When creating protocols, prefer request/response messages. The response should have an `error` bool and a `message` string with context.

* Log major events to stderr using `log/slog` at debug level. Include context like request IDs.

* Structure Go files with the most important code first (types, constructors, main logic before helpers).

* Prefer e2e tests over unit tests. Unit tests are for tricky code and edge cases that e2e can't easily reach. Don't write redundant tests covering the same path from different angles.

* Don't write tests that just exercise the standard library.

* Consolidate duplicated code before committing, not as a follow-up.

* Check for existing standards before inventing wire formats.

* Do not commit without allowing the user to review.

* Don't comment code just to describe what it's doing — use descriptive naming. Save comments for intent, purpose, and non-obvious design decisions.

* Any time `flake.nix` is changed, stop the session so the user can reload the development environment.

* When a fix is unverified or an assumption is uncertain, say so. Do not make premature commits.

* When asked to remove something, remove it. Don't argue to keep it.

## Environment

* Running on NixOS 25.11. Do not reference binaries by absolute path.

* Running inside a bubblewrap sandbox with write access only to this directory, its children, `/tmp`, and `/dev`. If access problems arise, stop and ask.

* If a shell command fails, stop and ask whether to install it or use an alternative.

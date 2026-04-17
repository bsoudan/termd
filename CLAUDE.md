# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test

Always use `make` to build. Do not run `go build` directly.

```bash
make                    # build everything (server, tui, termctl, mousehelper, upgrade binaries)
make build-server       # build server only → .local/bin/nxtermd
make build-tui          # build TUI client only → .local/bin/nxterm
make build-termctl      # build termctl CLI → .local/bin/nxtermctl
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

**nxtermd** is a terminal multiplexer with a client-server architecture. The server manages PTYs and terminal state; clients connect over the network to view and interact with terminals.

### Server (`internal/server/`, `cmd/nxtermd/`)

The server uses a **single event loop** for all mutable state (regions, clients, sessions). No mutexes protect server-level maps — all mutations go through `Server.requests` channel and are handled in `eventLoop()` (`server_requests.go`). Client and region goroutines communicate with the event loop via typed request structs with response channels.

- **Region** (`region.go`): wraps a PTY + child process + VT parser (`pkg/te`). A `readLoop` goroutine reads PTY output, feeds it through an `EventProxy`, and notifies subscribed clients. The Region holds a `sync.Mutex` only for its own screen state (snapshot reads).
- **Client** (`client.go`): wraps a `net.Conn`. Has a dedicated `writeLoop` goroutine that drains a `writeCh` channel, with backpressure detection (drops data and sends warnings if the client falls behind). A `readLoop` goroutine reads JSON messages and dispatches them to the server event loop.
- **Session** (`session.go`): groups regions. A session is created on first `session_connect_request` and spawns configured default programs.
- **EventProxy** (`event_proxy.go`): sits between the VT parser and the client write path. Captures terminal events into a batch, handles synchronized output mode (mode 2026) by buffering events until sync completes, then sending a full snapshot instead.
- **ServerTree** (`tree.go`): transactional mutation of regions/sessions/programs/clients. `StartTx()` → mutations → `CommitTx()` → returns version + accumulated `TreeOp[]`. After commit, `TreeEvents` are broadcasted to all clients for real-time sync.
- **Live upgrade** (`upgrade.go`, `upgrade_protocol.go`, `upgrade_recv.go`): supports zero-downtime server replacement. The old server serializes all state (regions, clients, sessions) to the new process over a Unix socket, including PTY file descriptors.

### TUI (`internal/tui/`, `cmd/nxterm/`)

Built on **bubbletea v2** with **lipgloss v2** for rendering.

- **Model** (`model.go`): top-level bubbletea model that owns a **layer stack**. Messages propagate top-down through layers; the first layer that handles a message stops propagation.
- **Layer interface** (`layer.go`): all UI components implement `TermdLayer` (extends `pkg/layer.Layer`). Layers return `[]*lipgloss.Layer` slices that get composited together.
- **MainLayer** (`mainlayer.go`): the base layer — manages the tab bar, terminal viewport, and region subscriptions.
- **Raw input** (`rawio.go`): after bubbletea Init completes, stdin is read directly and forwarded as `RawInputMsg` to avoid bubbletea's input parsing. This preserves exact terminal escape sequences for the remote PTY.
- **Tasks** (`pkg/layer/task.go`): synchronous goroutine abstraction over bubbletea's async event loop. Tasks call `Request()` for server roundtrips, `WaitFor(filter)` to wait for messages, and `PushLayer()`/`PopLayer()` to manage overlays — all as blocking calls. Use tasks for multi-step async workflows (e.g. upgrade); simple single-step overlays should stay as plain layers.
- **Overlay layers**: `connectlayer.go` (session picker), `commandpalette.go`, `helplayer.go`, `commandlayer.go`.

### Protocol (`internal/protocol/`, `protocol.md`)

Newline-delimited JSON over any transport. Request/response pattern: every request has a corresponding response with `error` bool + `message` string. Exceptions: `identify` and `input` are fire-and-forget. Server-initiated messages (`screen_update`, `terminal_events`, `region_created`, `region_destroyed`) have no response. The protocol is documented in `protocol.md`.

### Transport (`internal/transport/`)

Transport-agnostic `Listen(spec)` / `Dial(spec)` functions. Supported schemes: `unix:`, `tcp:`, `ws:`/`wss:`, `dssh:` (in-process Go SSH server), `ssh:` (client-only; spawns the system `ssh` binary in a PTY and runs `nxtermctl proxy` on the remote). All return `net.Listener`/`net.Conn` — the rest of the codebase is transport-unaware.

### Terminal Emulator (`pkg/te/`)

A full VT100/xterm terminal emulator library. `Stream` parses escape sequences, `Screen` maintains cell state with colors/attributes, `HistoryScreen` wraps Screen with scrollback. Extensively tested with esctest2 test suite ports and pyte compatibility tests.

### nxtermctl (`cmd/nxtermctl/`)

CLI admin tool for the server. Connects using `internal/client` and sends protocol messages (status, list regions, spawn, kill, etc.).

### Config (`internal/config/`)

TOML configuration. Server: `~/.config/nxtermd/server.toml`. Frontend: `~/.config/nxterm/config.toml`.


## Coding

* Never build code in tests — put build commands in the Makefile and use that.

* When writing tests, never use sleep unless absolutely necessary. Prefer explicit signaling/checking for readiness.

* When creating protocols, prefer request/response messages. The response should have an `error` bool and a `message` string with context.

* Log major events to stderr using `log/slog` at debug level. Include context like request IDs.

* Structure Go files with the most important code first (types, constructors, main logic before helpers).

* Prefer actor goroutines over shared state. Give mutable state a single owning goroutine and communicate via typed request/response channels instead of protecting it with mutexes. See `server.eventLoop()` and the region actor pattern.

* In the TUI, prefer the task abstraction (`pkg/layer/task.go`) over threading multi-step logic through bubbletea's async message loop. Tasks let you write sequential code (Request → WaitFor → PushLayer) that reads linearly instead of scattering steps across Update callbacks.

* Consolidate duplicated code before committing, not as a follow-up.

* Check for existing standards before inventing wire formats.

* When adding, removing, or updating an nxterm OSC sequence (OSC 2459 under the `nx` namespace), update `doc/nxterm-osc.md` in the same commit. Include the subcommand name, wire format, parser rules, and a changelog entry.

* Do not commit without allowing the user to review.

* Don't comment code just to describe what it's doing — use descriptive naming. Save comments for intent, purpose, and non-obvious design decisions.

* Any time `flake.nix` is changed, stop the session so the user can reload the development environment.

* When a fix is unverified or an assumption is uncertain, say so. Do not make premature commits.

* After each major change or new feature, re-read CLAUDE.md and update it if necessary.


## Testing

* Prefer e2e tests over unit tests. Unit tests are for tricky code and edge cases that e2e can't easily reach. Don't write redundant tests covering the same path from different angles.

* When fixing a bug, first write a failing test case, then fix the bug and ensure the failed test case now passes.

* Don't write tests that just exercise the standard library.

* All new features require an e2e test -- either extend an existing test case to cover the new feature, or add a new e2e test.

* When writing new tests, aim for reasonable test coverage.


## Environment

* Running on NixOS 25.11. Do not reference binaries by absolute path.

* Running inside a bubblewrap sandbox with write access only to this directory, its children, `/tmp`, and `/dev`. If access problems arise, stop and ask.

* If a shell command fails, stop and ask whether to install it or use an alternative.

# Milestone 5 Implementation Plan

Single step: replace the Zig server with a Go server.

## Implementation

### New Go server (`server/`)

4 files, 973 lines total (vs 1356 lines of Zig across 5 files):

**`main.go`** (87 lines): CLI flag parsing (`--socket`/`-s`, `--debug`/`-d`, `--help`), env var
fallbacks (`TERMD_SOCKET`, `TERMD_DEBUG`), signal handling (SIGTERM/SIGINT for graceful shutdown),
structured logging via the shared `termd/frontend/log` handler.

**`server.go`** (285 lines): Server struct with Unix socket listener. Accept loop in the main
goroutine. Regions (`map[string]*Region`) and clients (`map[uint32]*Client`) protected by
`sync.Mutex`. Per-region watcher goroutine listens on a notify channel for PTY output and handles
region death cleanup. Methods: SpawnRegion, DestroyRegion, FindRegion, Broadcast, KillRegion,
KillClient, sendScreenUpdate.

**`client.go`** (402 lines): One goroutine per client reads newline-delimited JSON via
`bufio.Scanner`, dispatches to typed handlers. Reuses `termd/frontend/protocol` types for both
parsing and serialization. Write mutex protects concurrent sends. All 11 message handlers match
the Zig server's behavior.

**`region.go`** (199 lines): PTY creation via `creack/pty`, child exec via `os/exec`. VT
terminal emulation via `go-te` (`te.NewScreen` + `te.NewStream`). Reader goroutine feeds PTY
output to `stream.FeedBytes`. `Snapshot()` returns `screen.Display()` lines + cursor position.
UUID v4 region IDs.

### Key design differences from Zig

| Aspect | Zig | Go |
|---|---|---|
| Concurrency | poll(2) event loop | Goroutines per client/region |
| Notifications | Notify pipe (fd) | Channel (`chan struct{}`) |
| Thread safety | std.Thread.Mutex + atomic | sync.Mutex |
| PTY | Manual posix_openpt/fork/exec | creack/pty + os/exec |
| VT terminal | ghostty-vt (Zig module) | go-te (pure Go) |
| Protocol types | Hand-written Zig structs + JSON | Shared Go package (frontend/protocol) |
| JSON serialization | Manual per-variant switch | json.Marshal with struct tags |

### Removed

- `server/build.zig`, `server/build.zig.zon`
- `server/src/*.zig` (main, protocol, server, client, region)
- Zig and ZLS from `flake.nix` packages
- `ZIG_GLOBAL_CACHE_DIR` from shell environment
- `.zig-cache` and `zig-out` from `.gitignore`

### Updated

- `Makefile`: `build-server` uses `go build`, removed `build-server-go` and `test-e2e-go`
- `flake.nix`: removed Zig/ZLS, removed Zig cache env var, updated echo messages
- `DESIGN.md`: stack table and key decisions reflect Go + go-te
- `server/go.mod`: module renamed from `termd/server-go` to `termd/server`

### Tests

All 19 existing e2e tests pass against the Go server with no test changes required.
The Go server is a drop-in replacement — same protocol, same socket, same behavior.

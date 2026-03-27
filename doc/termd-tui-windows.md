# termd-tui Windows Support

Goal: make `termd-tui` build and run on Windows, connecting to a
remote termd server over TCP, WebSocket, or SSH.  The server and termctl
remain Linux/macOS only.  Unix socket support is excluded on Windows.

## Background

The frontend is a BubbleTea TUI that connects to a termd server, sends
keystrokes, and renders terminal output.  Most of the code is already
cross-platform (BubbleTea, go-te, protocol, client).  Four areas need
platform-specific handling.

### How input interception works today (Unix)

BubbleTea normally reads stdin itself.  We need to intercept raw input so
we can route each byte to either the server or BubbleTea (for prefix-key
commands).  The current approach:

1. Put the terminal in raw mode (`term.MakeRaw`).
2. Fix up ONLCR output translation via `ioctl`/`termios`.
3. `syscall.Dup` stdin so we keep a readable handle.
4. Give BubbleTea an `io.Pipe` via `WithInput(pipeR)`.
5. A goroutine (`RawInputLoop`) reads the dup'd stdin, scans for the
   prefix key, and writes BubbleTea-bound bytes into the pipe.

### How it will work on Windows

On Windows, BubbleTea's ultraviolet layer detects a real console handle
and reads `INPUT_RECORD` events via `ReadConsoleInput`, bypassing the
`io.Reader` interface.  When given a pipe via `WithInput`, it falls back
to the byte-stream path — same as Unix.

Windows console input supports `ENABLE_VIRTUAL_TERMINAL_INPUT`, which
makes `ReadFile` on the console handle return VT escape sequences (same
byte format as a Unix terminal in raw mode).  So the Windows input loop
can:

1. Enable `ENABLE_VIRTUAL_TERMINAL_INPUT` on the console input handle.
2. `ReadFile`/`Read` raw VT bytes from the console handle.
3. Run the same prefix-key scanning logic.
4. Write BubbleTea-bound bytes into the pipe.

This keeps `RawInputLoop` platform-independent — only the terminal setup
and the stdin handle acquisition differ per platform.

## Files that need changes

| File | What changes | Why |
|------|-------------|-----|
| `frontend/ui/rawio.go` | Split into `rawio.go` (shared loop) + `rawio_unix.go` + `rawio_windows.go` (terminal setup) | `ioctl`/`termios` is Unix-only |
| `frontend/main.go` | Split stdin-dup into `stdin_unix.go` + `stdin_windows.go`; fix default endpoint and scheme detection | `syscall.Dup` is Unix-only; bare-path default assumes Unix sockets; `":"` detection breaks on Windows paths |
| `transport/transport.go` | `parseSpec` needs Windows-aware path handling; unix dial/listen should fail cleanly on Windows | Windows paths contain `:` (e.g. `C:\...`), breaking scheme detection |
| `transport/debug.go` | Split into `debug_unix.go` + `debug_windows.go` | `SIGUSR1` doesn't exist on Windows |

## Steps

### Step 1 — Split `SetupRawTerminal` by platform

Create build-tagged files for the terminal setup function.

**`rawio_unix.go`** (`//go:build !windows`):
- Current `SetupRawTerminal` as-is (`term.MakeRaw` + `ioctl` ONLCR fixup).

**`rawio_windows.go`** (`//go:build windows`):
- Get the console input handle (`GetStdHandle(STD_INPUT_HANDLE)`).
- Get current console mode, add `ENABLE_VIRTUAL_TERMINAL_INPUT`.
- Call `term.MakeRaw` for the raw-mode bits (it already supports Windows).
- Return a restore function that resets the console mode.
- No ONLCR fixup needed — Windows console output does `\n → \r\n` by
  default when `ENABLE_VIRTUAL_TERMINAL_PROCESSING` is enabled.

`RawInputLoop` and `sendInput` stay in the untagged `rawio.go` — they
only use `io.Reader`, `io.WriteCloser`, and the pipe.

### Step 2 — Split stdin handle acquisition by platform

The raw input loop needs a readable handle to the real console after
BubbleTea takes over stdin.

**`stdin_unix.go`** (`//go:build !windows`):
- `func dupStdin() (*os.File, error)` — wraps `syscall.Dup(os.Stdin.Fd())`.

**`stdin_windows.go`** (`//go:build windows`):
- `func dupStdin() (*os.File, error)` — use `DuplicateHandle` on the
  console input handle via `golang.org/x/sys/windows`, or simply return
  `os.Stdin` directly if BubbleTea only reads from the pipe and never
  touches the real console handle (test this — it may just work).

Update `main.go` to call `dupStdin()` instead of inline `syscall.Dup`.

### Step 3 — Fix default endpoint and scheme detection

The frontend defaults to `unix:/tmp/termd.sock` and infers `unix:` for
any address without a `:`.  Both break on Windows.

- Change the default: on Windows, require `TERMD_SOCKET` or `--socket`
  to be set explicitly (no silent default).  Print a clear error message
  if neither is provided, e.g.:
  `"error: --socket or TERMD_SOCKET required (e.g. tcp:host:port)"`.
- Fix `parseSpec`: a bare path starting with `/` or `.` → unix (existing).
  A Windows drive-letter path like `C:\...` should not be parsed as
  scheme `C` — but since we're not supporting unix sockets on Windows,
  this case should produce an error rather than silently misrouting.
- On Windows, remove the `unix:` auto-prefix fallback entirely.

Use build tags or a small platform constant (`const defaultScheme`) to
keep this clean.

### Step 4 — Platform-split `transport/debug.go`

**`debug_unix.go`** (`//go:build !windows`):
- Current `InstallStackDump` with `SIGUSR1`.

**`debug_windows.go`** (`//go:build windows`):
- No-op `InstallStackDump` (or use a different trigger if we want stack
  dumps on Windows later — not required now).

### Step 5 — Make `transport` unix-socket code build-tagged

Move the unix socket cases out of `transport.go` so the `net.Listen("unix", ...)`
and `net.Dial("unix", ...)` calls don't need to exist on Windows (they
may work on modern Windows, but we're explicitly not supporting them).

Options:
- **Preferred**: return a clear error `"unix sockets not supported on
  Windows"` from the `"unix"` case on Windows builds.  Simplest, no file
  split needed — just a build-tagged helper or a runtime `GOOS` check.
- Alternative: split into `transport_unix.go` / `transport_windows.go`.
  More code for little gain since Go's `net` package compiles unix
  sockets on all platforms anyway.

### Step 6 — Shell fallback on Windows

`main.go` falls back to `$SHELL` then `bash`.  On Windows:
- `$SHELL` is typically unset.
- Fall back to `cmd.exe` or `powershell.exe` (use `exec.LookPath`).
- This only matters for the `spawn` message sent to the server — the
  server must support the requested shell, so this is mostly cosmetic
  for the frontend.  But it should not crash if `$SHELL` is empty and
  `bash` isn't in `PATH`.

### Step 7 — CI cross-compilation check

Add a `GOOS=windows GOARCH=amd64 go build ./frontend/...` step to the
Makefile (or CI) to catch build breaks.  No need to run tests on Windows
yet — just verify it compiles.

## Out of scope

- Server (`termd`) on Windows — remains Linux/macOS only.
- `termctl` on Windows — remains Linux/macOS only.
- Unix socket transport on Windows.
- Running Windows tests in CI (cross-compile check is sufficient initially).
- Named pipe transport (future work if needed).

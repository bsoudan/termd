# Milestone 10 Implementation Plan

3 steps.

---

## Step 1: Server goroutine

Extract server communication from the bubbletea model into a dedicated goroutine.

### New type: `Server`

**`frontend/ui/server.go`**:

```go
type Server struct {
    ch chan any
}

func NewServer(bufSize int) *Server { ... }
func (s *Server) Send(msg any) { ... }  // non-blocking, drops if full
func (s *Server) Run(c *client.Client, p *tea.Program) { ... }
```

`Run` has two goroutines internally:
1. **Outbound loop**: reads from `s.ch`, dispatches to `c.Send()`. `InputMsg` gets
   base64-encoded. All other protocol types pass through directly.
2. **Inbound loop**: reads from `c.Updates()`, batches consecutive `TerminalEvents`,
   calls `p.Send(convertProtocolMsg(msg))`.

### Model changes

- Replace `client *client.Client` with `server *Server` in the Model struct
- Replace every `m.client.Send(...)` with `m.server.Send(...)`
- Remove `waitForUpdate(m.client)` from every handler — return `nil` instead
- Remove the drain loop in `TerminalEventsMsg` handler (batching moves to server goroutine)
- Remove `RegionReady chan string` — bubbletea stamps region ID on every outbound
  message, the server goroutine is a dumb pipe that doesn't track regions

### main.go changes

- Create `Server`, start `go server.Run(c, p)` before `p.Run()`
- Pass `server` to `NewModel` instead of `client`

### Tests

All existing e2e tests pass unchanged — the external behavior is identical.

---

## Step 2: Input goroutine and raw input handling in bubbletea

Replace the complex `RawInputLoop` with a trivial input goroutine and move all routing
logic into bubbletea's `Update()`.

### New input goroutine

**`frontend/ui/rawio.go`** (rewrite):

```go
type RawInputMsg []byte

func InputLoop(ctx context.Context, stdin *os.File, p *tea.Program) {
    buf := make([]byte, 4096)
    for {
        n, err := stdin.Read(buf)
        if n > 0 {
            raw := make([]byte, n)
            copy(raw, buf[:n])
            p.Send(RawInputMsg(raw))
        }
        if err != nil || ctx.Err() != nil {
            return
        }
    }
}
```

### Model changes — handling `RawInputMsg`

Add a `handleRawInput(msg RawInputMsg)` method to the model that contains the logic
currently in `RawInputLoop`:

1. **Overlay/scrollback keyboard mode**: if an overlay or scrollback (with keyboard nav)
   is active, write to the bubbletea input pipe so it arrives as `tea.KeyPressMsg`
2. **Prefix detection**: scan for ctrl+b. Bytes before it go to the server. Set
   `prefixActive`. The byte after ctrl+b is handled as a prefix command directly
   (no need to route through bubbletea's key parser).
3. **SGR mouse parsing**: detect `\x1b[<`, parse into `tea.MouseMsg`, call
   `handleMouse()` directly.
4. **Everything else**: forward to server via `server.Send(InputMsg{...})`

The pipe (`pipeR`/`pipeW`) is kept but narrowed: it only carries bytes that bubbletea's
internal input reader needs to parse (terminal query responses during startup, and key
events during overlay/scrollback focus mode).

### Remove from rawio.go

- `RawInputLoop` function (replaced by `InputLoop`)
- `sendInput`, `sendSplitMouseInput` (logic moves to model)
- `adjustMouseRow`, `parseSGRMouse`, `sgrToTeaButton` (move to model or a shared file)
- `FocusCh`, `ChildWantsMouse` parameters
- The `prefixStartedMsg` type (prefix state lives entirely in the model now)

### Remove from model

- `FocusCh chan chan struct{}`
- `ChildWantsMouse *atomic.Bool`
- `focusDone chan struct{}`
- The `prefixStartedMsg` handler in Update

### Tests

All existing e2e tests pass unchanged.

---

## Step 3: Clean shutdown

### stdin fd management

The input goroutine reads from a dup'd stdin fd. `os.File.Read()` is a blocking syscall
with no context-aware variant. To unblock it on shutdown, we close the dup'd fd. The
original stdin fd stays open for `term.Restore()` to write back the saved termios state.

```
stdinDup = dup(os.Stdin)   // input goroutine reads this
os.Stdin                    // kept open for term.Restore()
```

### Shutdown sequence

`main()` drives shutdown after `p.Run()` returns:

1. `p.Run()` returns (user detached, region destroyed, or error)
2. Close the server's send channel → server outbound loop exits
3. `c.Close()` → server inbound loop exits (Updates channel closes)
4. `stdinDup.Close()` → unblocks `stdin.Read()` → input loop exits
5. Close pipeW → bubbletea's input reader exits
6. `term.Restore(os.Stdin.Fd(), oldState)` — original fd is still valid
7. Wait on all goroutines (WaitGroup or channel) to confirm they exited

### Signal handling

- SIGINT/SIGTERM trigger `tea.Quit` or context cancellation
- Ensure no goroutine leaks by waiting on all goroutines before returning from main

### Tests

- Add a test that verifies clean shutdown: start server + frontend, send ctrl+b d,
  verify process exits with code 0 (no hang, no panic)
- The existing `TestPrefixKeyDetach` partially covers this but doesn't check for
  goroutine leaks

---

## Dependency graph

```
Step 1 (server goroutine)
  → Step 2 (input goroutine + raw input in bubbletea)
    → Step 3 (clean shutdown)
```

Each step is independently testable — all existing tests pass after each step.

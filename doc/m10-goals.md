# Milestone 10 Goals — Goroutine Architecture and Clean Shutdown

## 1. Three-goroutine architecture

Refactor termd-tui to have exactly three goroutines with clean message-passing boundaries:

**Input goroutine** — reads raw bytes from stdin, sends `RawInputMsg([]byte)` to bubbletea
via `p.Send()`. Stateless — no knowledge of regions, mouse mode, prefix state, or focus mode.
Accepts a context for cancellation on shutdown.

**Bubbletea goroutine** — managed by `tea.Run()`. Owns all application state and routing logic.
Receives `RawInputMsg` and decides what to do: scan for ctrl+b prefix, parse SGR mouse,
forward to the server, write to bubbletea's internal input pipe for terminal query responses
and key event parsing, or handle locally for overlays/scrollback. Also receives server
messages and renders the UI.

**Server goroutine** — owns the `*client.Client`. Exposes a `Send(msg any)` method (like
`program.Send`). Continuously reads from `c.Updates()` and calls `p.Send()` to deliver server
messages to bubbletea. Handles terminal event batching (replaces the drain loop in Update).

## 2. Eliminate shared state

Remove all shared mutable state between goroutines:
- Remove `ChildWantsMouse *atomic.Bool` — bubbletea owns mouse mode state
- Remove `FocusCh chan chan struct{}` — bubbletea owns focus state
- Remove `RegionReady chan string` — bubbletea stamps region ID on outbound messages
- Remove direct `client.Send()` calls from bubbletea — use `server.Send()` instead
- Remove `waitForUpdate` chain — server goroutine pumps messages continuously

## 3. Clean shutdown

All goroutines shut down gracefully:
- Input goroutine: cancelled via context (stdin read interrupted)
- Server goroutine: `Send` channel closed by bubbletea on exit, drains remaining messages,
  closes client connection
- Bubbletea: returns from `tea.Run()`, signals others to stop
- No goroutine leaks, no panic on closed channels, no orphaned PTY fds

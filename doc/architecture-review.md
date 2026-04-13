# Architectural Review: nxterm

**Date:** 2026-04-13

## Overall Assessment

This is a well-architected system. The layering is clean, there are zero circular
dependencies, and the core design decisions (single event loop, message-passing,
transport abstraction) are sound. What follows focuses on what could be improved.

---

## 1. `pkg/te/screen.go` — 2,894 lines, 114 methods

This is the largest file in the codebase by a wide margin and the most significant
structural issue.

**Problem:** `Screen` is a god struct. It implements the 82-method `EventHandler`
interface plus ~30 internal helpers, all in one file. The `EventHandler` interface
itself is monolithic — every consumer must implement all 82 methods, even if they
only care about cursor movement.

**Impact:**
- Hard to onboard: understanding Screen requires reading nearly 3,000 lines
- Extending with new VT features means making the big file bigger
- EventProxy must implement all 82 methods as pass-throughs, most of which are
  boilerplate
- Testing individual features (e.g. color handling vs cursor movement) means
  wading through unrelated code

**Suggestion:** Split `Screen` methods into logical groups in separate files
(`screen_cursor.go`, `screen_erase.go`, `screen_color.go`, `screen_mode.go`,
`screen_scroll.go`). This is purely organizational — no interface changes needed.
The interface itself could also be split into composable sub-interfaces
(`CursorHandler`, `ColorHandler`, etc.) that `EventHandler` embeds, making
EventProxy implementations more self-documenting.

---

## 2. `internal/tui/mainlayer.go` — 1,148 lines, 3+ responsibilities

**Problem:** MainLayer owns the event loop, session management, tree store, task
runner, connection lifecycle, command dispatch, and focus-mode buffering. It's the
TUI's god object.

**Impact:**
- The `Run()` method alone (lines 718–end) is a complex multi-phase select loop
  that's hard to modify without understanding all of its interactions
- Testing any single concern (e.g. reconnection) requires standing up the entire
  MainLayer
- Adding a new session management feature means editing a 1,148-line file

**Suggestion:** Extract concerns into focused types:
- `EventLoop` or `RunLoop` — the select loop in `Run()`, focus buffering
- `SessionManager` — active session tracking, session switching, tree store
- `ConnectionManager` — connect, reconnect, lifecycle events

MainLayer becomes a thin coordinator that wires these together.

---

## 3. Event loop request types are untyped (`chan any`)

The server's `requests chan any` accepts 26+ distinct struct types dispatched by a
type-switch with 37 case branches in `eventLoop()`.

**Problem:** No compile-time guarantee that a request type is handled. Adding a new
request type compiles fine even if you forget the case branch. The `any` channel
provides no documentation of what can be sent.

**Impact:** Moderate — the codebase is small enough that this is manageable today,
but it's a trap for future contributors.

**Suggestion:** Consider a `Request` interface with a method like
`handle(state *eventLoopState)` so each request carries its own handler. The event
loop becomes `for req := range requests { req.handle(state) }`. This catches
forgotten handlers at compile time and keeps each handler co-located with its
request struct.

---

## 4. Dual request/response patterns in the TUI

The TUI has two overlapping mechanisms for server round-trips:

1. **Manual `requestState`** — `nextReqID`, `pending map[uint64]ReplyFunc`, matched
   in `processServerMsg`
2. **Task system `Handle.Send()`** — the modern channel-bridge abstraction

**Problem:** Two ways to do the same thing. New code has to decide which to use. The
manual pattern leaks request tracking into MainLayer.

**Suggestion:** Migrate remaining manual request/response callsites to the task
system. If some are too simple to warrant a full task goroutine, add a one-shot
`Request(msg) (response, error)` helper on the session or connection that uses the
task machinery internally.

---

## 5. ScrollbackLayer is not in the layer stack

`TerminalLayer` owns a `scrollbackLayer *ScrollbackLayer` field and manually
delegates `Update`/`View` to it. This is a layer-within-a-layer that bypasses the
composition model.

**Impact:**
- ScrollbackLayer doesn't benefit from stack-level features (e.g. if you add focus
  tracking or input filtering at the stack level)
- TerminalLayer has extra conditional logic
  (`if sl := t.scrollbackLayer; sl != nil`) throughout its Update and View
- Inconsistent with how every other overlay (help, connect, command palette) works

**Suggestion:** Push ScrollbackLayer onto the main layer stack as a proper overlay.
TerminalLayer would push it on enter-scrollback and the stack would pop it on
QuitLayerMsg, eliminating the manual lifecycle.

---

## 6. Lazy TerminalLayer creation

`SessionLayer.ensureTerminal()` creates `TerminalLayer` instances on first message
receive rather than on tab creation.

**Problem:** Every codepath that touches `s.tabs[i].term` must either call
`ensureTerminal()` first or check for nil. This is easy to forget.

**Suggestion:** Create `TerminalLayer` when the tab is added in `syncFromTree()`.
The terminal starts empty either way — eager creation just removes nil checks.

---

## 7. `pkg/ultraviolet` — large, unclear boundary

The top-25 largest files include 7 from `pkg/ultraviolet/` totaling ~5,500+ lines.
This is a syntax highlighting library.

If ultraviolet is integral to the terminal rendering path, it's fine as `pkg/`. If
it's optional or experimental, consider moving it to `cmd/` or a separate module to
reduce cognitive load on contributors exploring the core.

---

## 8. Minor issues

| Issue | Location | Note |
|---|---|---|
| `internal/ui/` is an empty directory | `internal/ui/` | Remove it — it's confusing |
| `WriteProcessInput` callback on Screen | `pkg/te/screen.go` | Breaks the pure event-handler model. Consider making it a return value from the event methods that need it, or a separate `ResponseWriter` interface |
| Protocol `parsePayload()` is a 40+ case switch | `internal/protocol/protocol.go` | Grows linearly with new messages. A registry pattern (`map[string]func() any`) would scale better |
| No config validation | `internal/config/` | Types are parsed but never validated. Invalid listen addresses or negative timeouts pass silently |
| `client.go` message dispatch is 20+ cases | `internal/server/client.go` | Same pattern as protocol parsing — a handler registry would be cleaner |

---

## What's working well

These are real strengths, not platitudes:

- **Single event loop for server state** — eliminates an entire class of
  concurrency bugs. The fact that `Server` has no mutexes on its maps is a feature,
  not a shortcut.
- **Transport abstraction** — `Listen(spec)` / `Dial(spec)` returning
  `net.Listener` / `net.Conn` means the entire codebase is transport-unaware.
  Adding a new transport touches one package.
- **Protocol layer has zero internal dependencies** — clean data-only package.
- **Import graph is acyclic** — no circular dependencies across 11 internal
  packages. This is rare in Go projects of this size and speaks to disciplined
  layering.
- **Live upgrade** — transferring PTY FDs, screen state, and client connections
  across process boundaries is genuinely hard and it's well-implemented here.
- **e2e test harness** — PtyIO virtual screen with `WaitFor` polling is a much
  better pattern than sleep-based testing.
- **Backpressure design** — non-blocking broadcasts with silent drops for slow
  clients, blocking replies for request/response. The asymmetry is intentional and
  correct.
- **Raw input handling** — guaranteeing complete ANSI sequences per message is
  subtle and well-executed.

---

## Priority ranking

If spending time on this codebase, address these in order:

1. **Split `screen.go` into multiple files** — pure organizational refactor, high
   impact on readability, zero behavior change
2. **Extract concerns from MainLayer** — reduces the 1,148-line god object, makes
   TUI testable
3. **Unify request/response patterns** — eliminate the manual `requestState` in
   favor of the task system
4. **Push ScrollbackLayer into the layer stack** — makes the composition model
   consistent
5. **Typed request handling in server event loop** — compile-time safety for new
   request types

Items 1 and 4 are low-risk, high-reward refactors. Items 2 and 3 are higher effort
but pay off as the TUI grows.

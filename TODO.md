# TODO

## Report pixel size and graphics support in status dialog

When we add terminal graphics support, extend the `terminal:` section of the status dialog (`internal/tui/statuslayer.go`) with:

- **Pixel size** — window pixel dimensions and per-cell pixel size, queried via `CSI 14 t` (window) and `CSI 16 t` (cell).
- **Graphics protocols** — detected support for Sixel, Kitty graphics, and iTerm2 inline images. Sixel can be inferred from the DA1 response (attribute `4`); Kitty graphics is detected via an `APC G` probe; iTerm2 typically signals via `TERM_PROGRAM`.

## use urfav/cli for in-app commands

## Scrollback sync blocks input after exit

When the user enters scrollback on a session with large server-side history (e.g., 10000 lines), the server streams `GetScrollbackResponse` chunks (~1000 lines each, ~300ms apart). If the user exits scrollback while chunks are still streaming, the remaining chunks sit in bubbletea's message queue ahead of subsequent `RawInputMsg` and `TerminalEvents` messages. This causes typed characters to not appear until all chunks have been processed.

Possible fixes:
- Process scrollback responses outside bubbletea's message loop (in the Server.Run goroutine) and only send a single completion message to bubbletea
- Prioritize input and terminal event messages over scrollback responses in the message queue
- Cancel the server-side scrollback stream when scrollback exits

## Preserve client scrollback cache across reconnect

After a reconnect, the client currently refetches scrollback on the next entry even though the server's `ScrollbackTotal` lets us detect whether anything changed. Skipping the refetch is unsafe today because local scrollback rows don't carry an explicit `FirstSeq` — their seqs are derived as `TotalAdded() - Scrollback()`. If the client adopts a jumped-ahead `TotalAdded` from the reconnect `ScreenUpdate`, existing rows "slide" in seq space (previously attempted and broke `TestScrollbackAfterReconnectLarge`).

To enable cross-reconnect cache validation:
- Track `FirstSeq` explicitly in `HistoryScreen`, decoupled from `TotalAdded - Scrollback()`.
- On reconnect, compare cached end seq against fresh `ScrollbackTotal` from `ScreenUpdate`; refetch only the gap `[cachedEndSeq, serverTotal)` instead of the full history.

Also resolves items #1 and #2 in `scrollback-todo.md` (unify the two sync regimes, explicit `FirstSeq` tracking).

## Config validation

`internal/config/` parses TOML into typed structs but performs no constraint validation. Invalid listen addresses, negative timeouts, and other bad values pass silently. Add a validation pass after parsing that checks constraints and returns actionable errors.

## Alt-screen scrolls contaminate main-screen scrollback

`HistoryScreen.indexInternal` (pkg/te/history_screen.go:661) appends `h.Buffer[top]` to `h.history.Top` on every scroll-off, regardless of `altActive`. `Screen.enterAltScreen` swaps `Buffer`/`altBuffer` but leaves `history.Top` untouched (it lives on `HistoryScreen`, not `Screen`), so once alt screen is active the "scrolling off the top" path pushes *alt-buffer* rows into the shared main-screen history. Apps that scroll in alt screen (`less`, paged `man`, some TUI log viewers) leak rows; apps that absolutely-position (`vim`, `htop`) mostly don't.

The TUI's `scroll-up` keybinding is gated on `normal-screen` (internal/tui/session.go:447), so the user can't *enter* the scrollback viewer while in alt screen — that masks the symptom until they exit back to the main screen. `nxtermctl region scrollback` has no such guard and returns the polluted history any time.

`HistoryScreen.Resize` (added in `0a589a4`) inherits the same bug: shrinking while alt screen is active pushes rows from the alt buffer into `history.Top`.

Fix approach:

- Gate `history.Top.append` + `TotalAdded` / `FirstSeq` mutations in `indexInternal` on `!h.altActive`.
- Gate the shrink branch of `HistoryScreen.Resize` the same way.
- Cover with an e2e test: run `seq 1 200 | less`, quit, then assert the server's scrollback (via `nxtermctl region scrollback`) only contains the pre-`less` output — no `less`-rendered rows leaked in.

## Reflow wrapped lines on resize

`HistoryScreen.Resize` (and the embedded `Screen.Resize`) truncates rows wider than the new width and clears `lineWrapped`. A wrapped logical line — e.g. "HELLOWORLD" that autowrapped "HELLO" / "WORLD" across two rows at width 5 — stays as two disjoint rows after growing to width 10, instead of joining back into one row. Shrinking has the inverse problem: a single row wider than the new width gets truncated rather than spilling into a continuation row.

Modern emulators (iTerm2, Alacritty, kitty, WezTerm) reflow on resize so a resized session looks the same as if the output had arrived at the new width originally.

Needed work:

- Add per-row wrap state to scrollback (`history.Top.items` is `[][]Cell` today with no wrap flag), so a wrapped row scrolled off the screen retains its relationship to the next row.
- On resize, walk the combined scrollback + screen buffer, coalesce sequences of wrap-connected rows into logical lines, and re-split at the new width. Cursor position needs to follow its grapheme.
- Alt-screen apps (vim, less, htop) typically ignore reflow and just redraw on SIGWINCH — the reflow path should apply only to the main screen's scrollback + visible rows, not `altBuffer`.
- Cover with tests that exercise grow-then-shrink-back (should be lossless when the round-trip doesn't truncate) and content with mixed widths / grapheme clusters.

Related: the `pkg/te` CLAUDE.md calls out grapheme-cluster handling via `uniseg`; reflow must preserve cluster boundaries, not split mid-cluster.

## Simplify server state tree to eliminate shared state

Investigate reworking `ServerTree` so state ownership is unambiguous and duplication between live objects and tree nodes shrinks.

Today each tree entry pairs a live object (`Region`, `*Client`, …) with a protocol-form mirror (`protocol.RegionNode`, `protocol.ClientNode`). `SetRegion` rebuilds the mirror by calling accessors, some of which round-trip the actor (e.g. `ScrollbackLen()`) even when only event-loop-owned fields changed. Fields are split three ways — immutable, event-loop-owned, actor-owned — with the tree reaching into all three via an interface.

Questions to resolve:

- Can the protocol node be eliminated in favour of computing it on demand from a single authoritative source per field?
- For `SetRegion`, does a single actor round-trip returning an `ActorState` struct (scrollback length, future actor-owned fields) read more cleanly than 8 cheap accessors + 1 round-trip? What breaks when some of those fields are also event-loop-owned?
- If live scrollback length (or cursor position, or any fast-changing actor state) ever belongs in the tree, the update must flow actor → event loop → tree — not the other way. What's the right notification shape?
- Is the `Region` interface paying for itself, or does it encourage the shared-state pattern by hiding which goroutine owns each field?

Goal: a tree that's smaller, reads from one owner per field, and makes "who writes this" obvious at the call site. Related to the reliability audit's "uniform backpressure contract" — both are about making ownership boundaries explicit.

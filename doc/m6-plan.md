# Milestone 6 Implementation Plan

5 steps.

---

## Step 1: Upgrade e2e test harness to use go-te Screen

Add `github.com/rcarmo/go-te` to the e2e module. The `ptyIO` struct gets a local go-te
`Screen` + `Stream` that processes all PTY output. This gives tests access to the actual
rendered terminal state — what's visible at each row/col — instead of scanning raw ANSI bytes.

**`e2e/harness_test.go`**:
- `newPtyIO(ptmx, cols, rows)` creates a `te.NewScreen` and `te.NewStream`
- `readLoop` feeds all PTY data through `stream.FeedBytes` (under mutex)
- New methods:
  - `ScreenLines() []string` — current visible screen content
  - `ScreenLine(row) string` — single row
  - `WaitForScreen(check func([]string) bool, desc, timeout)` — wait until a
    condition on the screen state is met

**Rewrite `WaitFor`** to check the go-te Screen content (rendered text) instead of
accumulating raw bytes and scanning. This makes tests independent of ANSI encoding details.

**Remove**: `WaitForRaw`, `stripAnsi`, `findCursorPosition`, `findCursorInFrame` — all
superseded by the Screen-based approach.

**Rewrite tests to check screen positions.** Every test that checks content should verify
WHERE it appears, not just that it exists:

- `TestStartAndRender`: row 0 contains "bash" (tab bar), content rows below are non-empty
- `TestCursorPosition`: type "xy", verify adjacent on a specific row via ScreenLines, use
  termctl to verify server cursor position
- `TestLogViewerOverlay`: WaitForScreen checks `╭` and `╰` on specific rows, help text below
  bottom border, overlay height fits within 24 rows
- `TestPrefixKeyStatusIndicator`: "ctrl+b ..." appears on row 0 (tab bar)
- `TestInputRoundTrip`: "hello" appears on a content row (not row 0)
- `TestResize`: "80" appears on a content row

All 19 tests pass with the new harness on the current (v1) bubbletea.

---

## Step 2: Upgrade to bubbletea v2

- `charm.land/bubbletea/v2` replaces `github.com/charmbracelet/bubbletea`
- `charm.land/bubbles/v2` replaces `github.com/charmbracelet/bubbles`
- `charm.land/lipgloss/v2` replaces `github.com/charmbracelet/lipgloss`
- `View()` returns `tea.View` struct (set `AltScreen = true`, `Content` = rendered string)
- `tea.WithAltScreen()` removed from `NewProgram`, set in `View()` instead
- `tea.KeyMsg` → `tea.KeyPressMsg`
- `viewport.New(w, h)` → `viewport.New(viewport.WithWidth(w), viewport.WithHeight(h))`

Since tests now check the go-te Screen (which interprets bubbletea's ANSI output correctly
regardless of v1 vs v2 rendering differences), the upgrade should not break tests.

Fix any rendering issues that surface (CR/LF handling, overlay sizing).

All 19 tests pass.

---

## Step 3: Event-based protocol and server event capture

### Server: Event proxy

**`server/event_proxy.go`** (new): Implement `te.EventHandler` that wraps a real `te.Screen`.
Every method call:
1. Forwards to the underlying Screen
2. Appends a typed event struct to a batch

After feeding PTY bytes, the batch contains all events. The server sends them as a
`terminal_events` message, then clears the batch.

### Protocol

**`frontend/protocol/protocol.go`**: Add `TerminalEvents` and `ScreenSnapshot` message types.
Each event is a `TerminalEvent` with an `Op` string and typed fields.

### Server changes

**`server/region.go`**: Replace direct `te.Screen` with `EventProxy`. After each
`stream.FeedBytes`, collect events and send. On subscribe, send `screen_snapshot`.

### Test

New test: `TestTerminalEvents` — type a command, verify screen content updates correctly.

---

## Step 4: Frontend event replay and colored rendering

### Frontend: local Screen

**`frontend/ui/model.go`**: Add local `te.Screen` and `te.Stream`. On `TerminalEventsMsg`,
replay events. On `ScreenSnapshotMsg`, initialize from cell data.

### Frontend: ANSI rendering with colors

**`frontend/ui/render.go`**: Render cells with ANSI color/attribute sequences. Bubbletea v2's
Cursed Renderer diffs at the line level.

### Test

New test: `TestColorRendering` — run a colored command, verify content appears.

---

## Step 5: Update termctl and protocol spec

Update `termctl region view` to display colors. Update `protocol.md`.

---

## Event op mapping

go-te EventHandler method → protocol event op:

| EventHandler method | Op | Key fields |
|---|---|---|
| Draw(data) | draw | data |
| CursorPosition(row, col) | cursor_position | row, col |
| CursorUp/Down/Forward/Back | cursor_move | direction, count |
| ScrollUp(n) | scroll_up | count |
| ScrollDown(n) | scroll_down | count |
| EraseInDisplay(how) | erase_display | how, private |
| EraseInLine(how) | erase_line | how, private |
| InsertLines(n) | insert_lines | count |
| DeleteLines(n) | delete_lines | count |
| SelectGraphicRendition(attrs) | sgr | attrs, private |
| LineFeed | line_feed | |
| CarriageReturn | cr | |
| SetTitle(title) | set_title | data |
| SetMargins(top, bottom) | set_margins | params |
| Reset | reset | |

---

## Dependency Graph

```
Step 1 (test harness with go-te Screen)
  → Step 2 (bubbletea v2 upgrade)
    → Step 3 (event protocol + server proxy)
      → Step 4 (frontend replay + colored rendering)
        → Step 5 (termctl + protocol spec)
```

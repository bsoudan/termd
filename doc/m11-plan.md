# Milestone 11 Implementation Plan

4 steps. Each step extracts one group of concerns from the monolithic Model into a
self-contained component. All existing tests pass after each step.

---

## Step 1: Terminal component

Extract the core terminal display state into `TerminalComponent`.

### State to move

- `localScreen *te.Screen`
- `lines []string`
- `cursorRow`, `cursorCol int`
- `pendingClear bool`

### Methods

- `Update(msg) (TerminalComponent, tea.Cmd)` — handles `ScreenUpdateMsg`,
  `TerminalEventsMsg`. Contains `ReplayEvents` call and screen initialization.
- `View(width, height int, disconnected bool) string` — renders cell lines with
  cursor, replaces the `localScreen != nil` branch in `renderView`.
- `ChildWantsMouse() bool` — checks `localScreen.Mode` for mouse modes.
- `Snapshot() ([]string, int, int)` — returns display lines, cursor row/col for
  the parent to use in scrollback composition.

### Parent model changes

- `terminal TerminalComponent` field replaces `localScreen`, `lines`, `cursorRow`,
  `cursorCol`, `pendingClear`
- `Update()` delegates `ScreenUpdateMsg` and `TerminalEventsMsg` to `m.terminal.Update()`
- `View()` calls `m.terminal.View()` for the content area
- `handleMouse()` calls `m.terminal.ChildWantsMouse()` instead of direct Mode lookups

---

## Step 2: Scrollback component

Extract scrollback navigation into `ScrollbackComponent`.

### State to move

- `scrollbackMode bool`
- `scrollbackOffset int`
- `scrollbackCells [][]protocol.ScreenCell`

### Methods

- `Update(msg tea.KeyPressMsg) (ScrollbackComponent, tea.Cmd)` — handles arrow keys,
  pgup/pgdn, home/end, q/esc. Returns an `ExitScrollbackMsg` when the user exits.
- `HandleWheel(button tea.MouseButton) ScrollbackComponent` — adjusts offset,
  returns `ExitScrollbackMsg` when scrolling past 0.
- `View(screenCells [][]te.Cell, width, height int) string` — renders combined
  scrollback + screen buffer.
- `Active() bool`
- `Enter(offset int) ScrollbackComponent` — activates, returns request cmd.
- `SetData(cells [][]protocol.ScreenCell) ScrollbackComponent`
- `StatusText() string` — returns `"scrollback [offset/total]"` for the tab bar.

### Parent model changes

- `scrollback ScrollbackComponent` field replaces the three scrollback fields
- `Update()` delegates to `m.scrollback.Update()` when active
- `handleMouse()` delegates wheel events to `m.scrollback.HandleWheel()`
- Tab bar reads `m.scrollback.StatusText()`

---

## Step 3: Overlay component

Unify log viewer, changelog, status, and help into a shared overlay system.

### Types

```go
type OverlayKind string // "log", "changelog", "status", "help"

type OverlayComponent struct {
    kind       OverlayKind
    vp         viewport.Model
    hScroll    int
    // Status-specific
    serverStatus *protocol.StatusResponse
    // Help-specific
    helpCursor int
    helpItems  []helpItem
}
```

### State to move

- `overlayMode string`
- `overlayVP viewport.Model`
- `overlayHScroll int`
- `showStatus bool`, `serverStatus *protocol.StatusResponse`
- `showHelp bool`, `helpCursor int`
- The `helpItems` slice

### Methods

- `Update(msg tea.KeyPressMsg) (OverlayComponent, tea.Cmd)` — handles navigation keys.
  Returns `CloseOverlayMsg` when the user exits. Help-specific: handles up/down/enter
  for selection. Status-specific: just q/esc.
- `HandleWheel(msg tea.MouseWheelMsg) (OverlayComponent, tea.Cmd)` — scroll in viewport.
- `View(width, height int) string` — renders the appropriate overlay dialog.
- `Active() bool`
- `Kind() OverlayKind`
- `OpenLog(content string)`, `OpenChangelog(content string)`, `OpenStatus()`,
  `OpenHelp()` — constructors/openers.
- `SetStatusData(resp *protocol.StatusResponse)`

### Parent model changes

- `overlay OverlayComponent` field replaces all overlay/status/help fields
- `Update()` delegates to `m.overlay.Update()` when active
- Tab bar reads `m.overlay.Kind()` for the right-side label
- `renderView()` calls `m.overlay.View()` when active, composited over the base

---

## Step 4: Parent model cleanup

With components extracted, the parent Model becomes a thin router.

### Remaining parent state

```go
type Model struct {
    server     *Server
    pipeW      io.Writer

    // Mode determines message routing
    prefixMode bool

    // Components
    terminal   TerminalComponent
    scrollback ScrollbackComponent
    overlay    OverlayComponent

    // Connection/session state
    regionID   string
    regionName string
    connStatus string
    retryAt    time.Time
    Endpoint   string

    // Terminal capabilities (from bubbletea)
    termEnv       map[string]string
    keyboardFlags int
    bgDark        *bool
    localHostname string

    // Chrome
    Version   string
    Changelog string
    LogRing   *termlog.LogRingBuffer
    showHint  bool
    termWidth  int
    termHeight int

    Detached bool
}
```

### Parent Update

The routing becomes a clean switch:

```go
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case RawInputMsg:
        return m.handleRawInput(msg)

    // Server messages → delegate to appropriate component
    case ScreenUpdateMsg:
        m.terminal, cmd = m.terminal.Update(msg)
        return m, cmd
    case TerminalEventsMsg:
        m.terminal, cmd = m.terminal.Update(msg)
        return m, cmd
    case ScrollbackResponseMsg:
        m.scrollback = m.scrollback.SetData(msg.Lines)
        return m, nil
    case protocol.StatusResponse:
        m.overlay.SetStatusData(&msg)
        return m, nil

    // Component exit messages
    case ExitScrollbackMsg:
        m.scrollback = ScrollbackComponent{} // reset
        return m, nil
    case CloseOverlayMsg:
        m.overlay = OverlayComponent{} // reset
        return m, nil

    // Input routing based on active component
    case tea.MouseMsg:
        return m.handleMouse(msg)
    case tea.KeyPressMsg:
        if m.overlay.Active() {
            m.overlay, cmd = m.overlay.Update(msg)
        } else if m.scrollback.Active() {
            m.scrollback, cmd = m.scrollback.Update(msg)
        } else {
            return m.handlePrefixOrInput(msg)
        }
        return m, cmd

    // ... window size, env, etc.
    }
}
```

### Parent View

```go
func renderView(m Model) string {
    base := renderTabBar(...) + "\n" + m.terminal.View(width, height, disconnected)
    if m.scrollback.Active() {
        base = renderTabBar(...) + "\n" + m.scrollback.View(m.terminal.ScreenCells(), width, height)
    }
    if m.overlay.Active() {
        base = m.overlay.View(base, width, height) // composited over base
    }
    return base
}
```

### Cleanup

- Move rendering helpers (`renderCellLine`, `sgrTransition`, etc.) into the terminal
  component's file or a shared `render_helpers.go`
- Move overlay rendering (`renderScrollableOverlay`, `renderStatusOverlay`, etc.)
  into the overlay component's file
- The parent's `model.go` should be mostly routing — under 300 lines

### Tests

All existing e2e tests pass. No new tests needed — this is a pure refactor.

---

## Dependency graph

```
Step 1 (terminal component)
  → Step 2 (scrollback component)
  → Step 3 (overlay component)
    → Step 4 (parent cleanup)
```

Steps 1-3 are independent of each other but step 4 depends on all three.

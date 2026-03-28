# Milestone 12 Implementation Plan

4 steps.

---

## Layer interface

```go
type Layer interface {
    Update(tea.Msg) (tea.Msg, bool)              // response, handled
    View(width, height int) *lipgloss.Layer       // nil = transparent
    Status() (text string, bold bool, red bool)   // empty text = defer to layer below
}
```

Layers are pointers with mutable state — Update mutates in place. The bool
return signals whether the message was handled (stop propagating) or should
continue to the next layer.

The `tea.Msg` return is how layers communicate back to the model:
- `nil` — no follow-up
- `tea.Cmd` — async work for bubbletea to run (result feeds back next cycle)
- `QuitLayerMsg{}` — model pops this layer immediately (same cycle, no flicker)

Pushing is async: the layer returns a `tea.Cmd` that produces a `PushLayerMsg`.
The model sees it at the top of the next Update and appends to the stack.

Messages iterate **top-down**: the topmost layer gets first crack. If it returns
`handled = true`, the loop breaks and lower layers never see the message.
Server messages (TerminalEvents, ScreenUpdate) pass through overlays unhandled
and reach session at the bottom.

```go
type QuitLayerMsg struct{}
type PushLayerMsg struct{ Layer Layer }
```

## Terminal interface

Terminals are children of the session layer, not on the layer stack.
Session owns, routes to, and renders them.

```go
type Terminal interface {
    Update(tea.Msg) tea.Msg
    View(width, height int) string
    Title() string                              // tab bar label ("bash", "python")
    Status() (text string, bold bool, red bool) // per-terminal status
}
```

Differences from Layer:
- No `handled bool` — session decides what to route, terminal always processes
- No `below string` in View — renders its own content, session composes it
- Simpler `tea.Msg` return — just follow-up messages, no stack manipulation

Scrollback is a mode of the terminal, not a separate layer. The terminal holds
scrollback state and switches what it renders and reports in Status().

## Model

```go
type Model struct {
    layers    []Layer
    pending   map[uint64]ReplyFunc
    nextReqID uint64
    Version   string
    Detached  bool
}
```

Model.Update:
```go
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    // Handle PushLayerMsg (append to stack)
    // Unwrap protocol.Message (req_id matching)

    var cmds []tea.Cmd
    for i := len(m.layers) - 1; i >= 0; i-- {
        resp, handled := m.layers[i].Update(msg)
        switch r := resp.(type) {
        case QuitLayerMsg:
            m.layers = slices.Delete(m.layers, i, i+1)
        case tea.Cmd:
            if r != nil {
                cmds = append(cmds, r)
            }
        }
        if handled {
            break
        }
    }
    return m, tea.Batch(cmds...)
}
```

Model.View:
```go
func renderView(m Model) string {
    var layers []lipgloss.Layer
    for _, layer := range m.layers {
        if l := layer.View(width, height); l != nil {
            layers = append(layers, *l)
        }
    }

    // Stamp status text + branding onto top-right of row 0
    text, bold, red := topLayerStatus(m.layers)
    layers = append(layers, renderStatusOverlay(text, bold, red, width))

    return lipgloss.NewCompositor(layers...).Render()
}
```

Session's layer includes the tab bar (row 0) with terminal tabs on the left.
The model stamps the right side of row 0 on top — the topmost non-empty
`Status()` from the layer stack plus "termd-tui" branding.

Each layer returns a positioned `lipgloss.Layer` — session returns a
full-screen layer (tab bar + terminal content), overlays return centered
dialog layers with higher Z. Transparent layers (command, hint) return nil.
The compositor handles all stacking.

---

## Step 1: Session layer

The root layer, always on the stack. Owns server communication, region
lifecycle, and terminal children.

### State
- server *Server
- terminals []*TerminalImpl
- active int
- regionID, regionName, cmd, cmdArgs
- connStatus, retryAt
- status, err string
- logRing *LogRingBuffer

### Update handles
- ListRegionsResponse → spawn or subscribe
- SpawnResponse → subscribe
- SubscribeResponse → create terminal, add to terminals list
- ScreenUpdate / TerminalEvents → route to correct terminal by regionID
- DisconnectedMsg / ReconnectedMsg / reconnectTickMsg
- ServerErrorMsg → signal app exit
- LogEntryMsg → handled (triggers re-render; log viewer reads ring on View)
- RawInputMsg → forward to active terminal
- MouseMsg → forward to active terminal
- WindowSizeMsg → forward to all terminals

### View
- Returns full-screen layer: tab bar (row 0) + content below
- Tab bar left side: terminal tabs with active highlighted
- When no terminals: content is status text ("connecting...", "spawning...", etc.)
- When terminals exist: content is active terminal's rendered output

### Status
- Reconnecting: text="reconnecting to X in Ns...", bold=true, red=true
- Normal: text=endpoint, bold=false, red=false

### Tab titles
- Exposes terminal list for model to build tab bar
- Provides active index for highlighting

---

## Step 2: Terminal (as session child)

Implements the Terminal interface. Owns screen state, capabilities,
scrollback mode. Not on the layer stack.

### State
- screen, cursor, lines (current Terminal struct)
- scrollback (current Scrollback struct)
- termWidth, termHeight
- termEnv, keyboardFlags, bgDark, localHostname
- pipeW (for overlay focus mode raw input forwarding)
- server reference (for sending input/resize)
- regionID

### Update handles
- ScreenUpdate / GetScreenResponse → update screen
- TerminalEvents → replay on screen
- WindowSizeMsg → store dimensions, send resize via server
- KeyboardEnhancementsMsg, BackgroundColorMsg, EnvMsg
- RawInputMsg → detect ctrl+b (return PushLayerMsg for CommandLayer),
  forward rest to server. If scrollback active, write to pipeW.
- MouseMsg → forward to server when child wants mouse, scroll wheel
  enters/exits scrollback mode

### View
- If scrollback active: render scrollback + screen combined view
- Otherwise: render terminal content (cells + cursor)

### Title
- Region name ("bash", "python", etc.)

### Status
- If scrollback active: text="scrollback [n/n]", bold=true
- Otherwise: empty

---

## Step 3: CommandLayer + HintLayer

### CommandLayer (temporary)

Pushed by terminal when ctrl+b is detected in RawInputMsg.

- Update: captures next RawInputMsg byte, dispatches command:
  - 'd' → detach (session handles app exit)
  - 'l' → push LogViewerLayer
  - 's' → push StatusLayer
  - '?' → push HelpLayer
  - 'n' → push ReleaseNotesLayer
  - 'r' → refresh screen
  - '[' → enter scrollback mode on active terminal
  - ctrl+b → forward literal ctrl+b
  - All cases: return QuitLayerMsg to pop self after dispatch
- View: nil (transparent)
- Status: "?", bold=true

### HintLayer (temporary)

Pushed at startup by Init().

- Update: handles hideHintMsg → return QuitLayerMsg. Ignores everything else.
- View: transparent
- Status: "ctrl+b ? for help", bold=true
- On push, returns tea.Cmd that fires hideHintMsg after 3 seconds

---

## Step 4: Overlays as layers

### LogViewerLayer
- State: viewport + hscroll + logRing reference
- Update: keyboard nav (arrows, q/esc, left/right). RawInputMsg written to
  pipeW for key event parsing.
- View: returns centered dialog layer (higher Z). Reads current content
  from logRing on each View() call.
- Status: "logviewer", bold=true
- Returns QuitLayerMsg on q/esc

### ReleaseNotesLayer
- State: viewport + hscroll + changelog string
- Changelog passed at construction
- Update: keyboard nav (arrows, q/esc, left/right). RawInputMsg written to
  pipeW.
- View: returns centered dialog layer (higher Z)
- Status: "release notes", bold=true
- Returns QuitLayerMsg on q/esc

### StatusLayer
- State: StatusCaps + *StatusResponse
- On creation: sends status request via server (with reply handler that
  populates response data)
- Update: q/esc → QuitLayerMsg. RawInputMsg written to pipeW.
- View: returns centered status dialog layer (higher Z)
- Status: "status", bold=true

### HelpLayer
- State: cursor, items
- Update: up/down/enter/q, shortcut keys. RawInputMsg written to pipeW.
- View: returns centered help dialog layer (higher Z)
- Status: "help", bold=true
- On selection: returns QuitLayerMsg to pop self, returns tea.Cmd that
  executes the selected action (which might push another layer)

---

## Dependency graph

```
Step 1 (session layer)
  → Step 2 (terminal as session child)
    → Step 3 (command + hint layers)
      → Step 4 (overlays as layers)
```

Each step is independently testable — all existing e2e tests pass after each.

---

## What the model becomes

```go
type Model struct {
    layers    []Layer
    pending   map[uint64]ReplyFunc
    nextReqID uint64
    Version   string
    Detached  bool
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    // Handle PushLayerMsg (append to stack)
    // Unwrap protocol.Message (req_id matching)
    // Iterate layers top-down, collect cmds, pop on QuitLayerMsg
}

func (m Model) View() tea.View {
    // Collect lipgloss.Layer from each layer, composite
    // Stamp topmost Status() + branding onto top-right of row 0
}
```

Under 100 lines.

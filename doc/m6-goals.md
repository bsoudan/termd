# Milestone 6 Goals — Optimized Rendering with Color and Attributes

Replace full-screen snapshots with an event-based protocol and add full 24-bit RGB color and
text attribute support. Upgrade to bubbletea v2 for its ncurses-quality renderer.

## 1. Event-based screen updates

Replace `screen_update` (full lines[] snapshot) with `terminal_events` — a stream of typed
terminal operations. The server intercepts go-te's EventHandler calls and forwards them as
protocol messages. The frontend replays events into its own local go-te Screen and renders
from that.

Events include: draw, cursor movement, scroll, erase, insert/delete lines, SGR (color/style
changes), resize, bell, title changes.

Benefits:
- Typing a character sends one draw event, not 24 lines of text
- Scrolling sends a scroll event, not a full screen redraw
- Colors and attributes come naturally from SGR events

## 2. Full 24-bit RGB color and text attributes

The event stream carries SGR (Select Graphic Rendition) attribute data:
- Foreground/background: 24-bit RGB, 256-color palette, or default
- Bold, italic, underline, strikethrough, reverse, dim, blink

The frontend renders cells with ANSI escape sequences matching the attributes.

## 3. Bubbletea v2 upgrade

Upgrade from bubbletea v1 to v2 for the Cursed Renderer — an ncurses-quality rendering
engine that automatically optimizes terminal output (line diffing, minimal repaints). This
makes the event-replay + View() approach efficient without manual scroll region management.

## 4. Initial state sync

When a frontend connects (subscribe), the server sends a full screen snapshot as the initial
state. After that, only events are sent. This replaces the current `screen_update` on
subscribe.

The snapshot message carries the full cell grid with colors/attributes so the frontend starts
with the correct state.

## Protocol changes

Replace `screen_update` with:

```
terminal_events     type, region_id, events[]
```

Where each event is a typed operation:
```json
{"op": "draw", "data": "hello"}
{"op": "sgr", "attrs": [1, 32]}
{"op": "cursor_position", "row": 5, "col": 10}
{"op": "scroll_up", "count": 1}
{"op": "erase_in_display", "how": 2}
{"op": "insert_lines", "count": 1}
...
```

Add a new full-state snapshot message for initial sync:
```
screen_snapshot     type, region_id, cursor_row, cursor_col, cells[][]
```

Where each cell has `{char, fg, bg, attrs}`.

Retain `get_screen_response` for termctl but update it to include color data.

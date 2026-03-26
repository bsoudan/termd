# termd Protocol Specification

Wire format: newline-delimited JSON. Each message is one UTF-8 JSON object followed by `\n`.

All request messages have a corresponding response. The `input` and `identify` messages are
fire-and-forget exceptions. The response always includes `error` (bool) and `message` (string)
fields. `message` is empty on success and contains a human-readable description with context on
failure.

Server-initiated messages (`region_created`, `screen_update`, `region_destroyed`) have no response.

---

## Frontend → Server

### identify

Identify the connecting client to the server. Fire-and-forget; no response.

```json
{ "type": "identify", "hostname": "myhost", "username": "alice", "pid": 12345, "process": "termd-frontend" }
```

| Field    | Type   | Description                    |
|----------|--------|--------------------------------|
| type     | string | `"identify"`                   |
| hostname | string | Client hostname                |
| username | string | Client username                |
| pid      | int32  | Client process ID              |
| process  | string | Client process name            |

---

### spawn_request

Spawn a new program in a new region.

```json
{ "type": "spawn_request", "cmd": "/bin/bash", "args": [] }
```

| Field | Type     | Description                  |
|-------|----------|------------------------------|
| type  | string   | `"spawn_request"`            |
| cmd   | string   | Executable path              |
| args  | []string | Arguments (may be empty)     |

### spawn_response

```json
{ "type": "spawn_response", "region_id": "abc123", "name": "bash", "error": false, "message": "" }
```

| Field     | Type   | Description                                  |
|-----------|--------|----------------------------------------------|
| type      | string | `"spawn_response"`                           |
| region_id | string | Assigned region ID (empty on error)          |
| name      | string | Human-readable region name (empty on error)  |
| error     | bool   | True if the spawn failed                     |
| message   | string | Error description with context, or `""`      |

The server also broadcasts a `region_created` message to all connected clients after a successful
spawn (see below).

---

### subscribe_request

Subscribe to screen updates for a region.

```json
{ "type": "subscribe_request", "region_id": "abc123" }
```

| Field     | Type   | Description       |
|-----------|--------|-------------------|
| type      | string | `"subscribe_request"` |
| region_id | string | Region to watch   |

### subscribe_response

```json
{ "type": "subscribe_response", "region_id": "abc123", "error": false, "message": "" }
```

On success, the server writes a `screen_update` with the current screen state (including per-cell
color data) to the wire first, then writes this `subscribe_response`. The frontend will therefore
always receive a `screen_update` before the `subscribe_response` for a given subscription. After
the initial `screen_update`, all subsequent changes are sent as `terminal_events` messages.

| Field     | Type   | Description                      |
|-----------|--------|----------------------------------|
| type      | string | `"subscribe_response"`           |
| region_id | string | Echoed region ID                 |
| error     | bool   | True if the region does not exist |
| message   | string | Error description, or `""`       |

---

### input

Send raw input bytes to a region's PTY. Fire-and-forget; no response.

```json
{ "type": "input", "region_id": "abc123", "data": "<base64>" }
```

| Field     | Type   | Description                    |
|-----------|--------|--------------------------------|
| type      | string | `"input"`                      |
| region_id | string | Target region                  |
| data      | string | Base64-encoded bytes           |

---

### resize_request

Resize a region's PTY and terminal.

```json
{ "type": "resize_request", "region_id": "abc123", "width": 220, "height": 49 }
```

| Field     | Type   | Description               |
|-----------|--------|---------------------------|
| type      | string | `"resize_request"`        |
| region_id | string | Target region             |
| width     | uint16 | New width in columns      |
| height    | uint16 | New height in rows        |

Note: the frontend subtracts 1 from the terminal height to account for the tab bar row.

### resize_response

```json
{ "type": "resize_response", "region_id": "abc123", "error": false, "message": "" }
```

| Field     | Type   | Description                 |
|-----------|--------|-----------------------------|
| type      | string | `"resize_response"`         |
| region_id | string | Echoed region ID            |
| error     | bool   | True if the resize failed   |
| message   | string | Error description, or `""`  |

---

### list_regions_request

List all existing regions on the server. Used by the frontend on startup to check for sessions
to resume.

```json
{ "type": "list_regions_request" }
```

| Field | Type   | Description            |
|-------|--------|------------------------|
| type  | string | `"list_regions_request"` |

### list_regions_response

```json
{ "type": "list_regions_response", "regions": [{"region_id": "abc123", "name": "bash", "cmd": "/bin/bash", "pid": 42}], "error": false, "message": "" }
```

| Field   | Type           | Description                                                    |
|---------|----------------|----------------------------------------------------------------|
| type    | string         | `"list_regions_response"`                                      |
| regions | []RegionInfo   | Array of `{region_id, name, cmd, pid}` for each live region   |
| error   | bool           | True on failure                                                |
| message | string         | Error description, or `""`                                     |

RegionInfo fields:

| Field     | Type   | Description                |
|-----------|--------|----------------------------|
| region_id | string | Region ID                  |
| name      | string | Human-readable region name |
| cmd       | string | Command that was spawned   |
| pid       | int32  | Child process ID           |

---

### status_request

Query the server's status.

```json
{ "type": "status_request" }
```

| Field | Type   | Description          |
|-------|--------|----------------------|
| type  | string | `"status_request"`   |

### status_response

```json
{ "type": "status_response", "pid": 1234, "uptime_seconds": 3600, "socket_path": "/tmp/termd.sock", "num_clients": 2, "num_regions": 1, "error": false, "message": "" }
```

| Field          | Type   | Description                        |
|----------------|--------|------------------------------------|
| type           | string | `"status_response"`                |
| pid            | int32  | Server process ID                  |
| uptime_seconds | int64  | Server uptime in seconds           |
| socket_path    | string | Path to the Unix socket            |
| num_clients    | uint32 | Number of connected clients        |
| num_regions    | uint32 | Number of active regions           |
| error          | bool   | True on failure                    |
| message        | string | Error description, or `""`         |

---

### get_screen_request

Fetch the current screen contents of a region without subscribing.

```json
{ "type": "get_screen_request", "region_id": "abc123" }
```

| Field     | Type   | Description              |
|-----------|--------|--------------------------|
| type      | string | `"get_screen_request"`   |
| region_id | string | Target region            |

### get_screen_response

```json
{ "type": "get_screen_response", "region_id": "abc123", "cursor_row": 0, "cursor_col": 2, "lines": ["$ "], "cells": [[...]], "error": false, "message": "" }
```

| Field      | Type           | Description                                                     |
|------------|----------------|-----------------------------------------------------------------|
| type       | string         | `"get_screen_response"`                                         |
| region_id  | string         | Echoed region ID                                                |
| cursor_row | uint16         | 0-indexed cursor row                                            |
| cursor_col | uint16         | 0-indexed cursor column                                         |
| lines      | []string       | One string per row, space-padded to width, no escape sequences  |
| cells      | [][]ScreenCell | Optional. Per-cell color/attribute data (see ScreenCell above)  |
| error      | bool           | True if the region does not exist                               |
| message    | string         | Error description, or `""`                                      |

---

### kill_region_request

Kill a region's child process.

```json
{ "type": "kill_region_request", "region_id": "abc123" }
```

| Field     | Type   | Description              |
|-----------|--------|--------------------------|
| type      | string | `"kill_region_request"`  |
| region_id | string | Region to kill           |

### kill_region_response

```json
{ "type": "kill_region_response", "region_id": "abc123", "error": false, "message": "" }
```

| Field     | Type   | Description                 |
|-----------|--------|-----------------------------|
| type      | string | `"kill_region_response"`    |
| region_id | string | Echoed region ID            |
| error     | bool   | True if region not found    |
| message   | string | Error description, or `""`  |

---

### list_clients_request

List all connected clients.

```json
{ "type": "list_clients_request" }
```

| Field | Type   | Description              |
|-------|--------|--------------------------|
| type  | string | `"list_clients_request"` |

### list_clients_response

```json
{ "type": "list_clients_response", "clients": [{"client_id": 1, "hostname": "myhost", "username": "alice", "pid": 12345, "process": "termd-frontend", "subscribed_region_id": "abc123"}], "error": false, "message": "" }
```

| Field   | Type           | Description                           |
|---------|----------------|---------------------------------------|
| type    | string         | `"list_clients_response"`             |
| clients | []ClientInfo   | Array of client info objects          |
| error   | bool           | True on failure                       |
| message | string         | Error description, or `""`            |

ClientInfo fields:

| Field                | Type   | Description                              |
|----------------------|--------|------------------------------------------|
| client_id            | uint32 | Client ID                                |
| hostname             | string | Client hostname                          |
| username             | string | Client username                          |
| pid                  | int32  | Client process ID                        |
| process              | string | Client process name                      |
| subscribed_region_id | string | Region the client is subscribed to, or `""` |

---

### kill_client_request

Disconnect a client by ID.

```json
{ "type": "kill_client_request", "client_id": 1 }
```

| Field     | Type   | Description              |
|-----------|--------|--------------------------|
| type      | string | `"kill_client_request"`  |
| client_id | uint32 | Client to kill           |

### kill_client_response

```json
{ "type": "kill_client_response", "client_id": 1, "error": false, "message": "" }
```

| Field     | Type   | Description                       |
|-----------|--------|-----------------------------------|
| type      | string | `"kill_client_response"`          |
| client_id | uint32 | Echoed client ID                  |
| error     | bool   | True if client not found or self  |
| message   | string | Error description, or `""`        |

---

## Server → Frontend

### region_created

Broadcast to all connected clients when a new region is spawned. Sent after `spawn_response`.

```json
{ "type": "region_created", "region_id": "abc123", "name": "bash" }
```

| Field     | Type   | Description        |
|-----------|--------|--------------------|
| type      | string | `"region_created"` |
| region_id | string | New region ID      |
| name      | string | Region name        |

---

### screen_update

Sent on subscribe as the initial screen snapshot. Contains the full screen as plain text plus
optional per-cell color and attribute data. The server's VT parser (go-te) renders all terminal
escape sequences into an internal screen buffer; the server extracts the visible characters and
their attributes.

```json
{
  "type": "screen_update",
  "region_id": "abc123",
  "cursor_row": 2,
  "cursor_col": 2,
  "lines": ["$ echo hello", "hello", "$ _"],
  "cells": [
    [{"c": "$"}, {"c": " "}, {"c": "e"}, ...],
    [{"c": "h", "fg": "green"}, {"c": "e", "fg": "green"}, ...],
    [{"c": "$"}, {"c": " "}, {"c": "_", "a": 1}]
  ]
}
```

| Field      | Type             | Description                                                        |
|------------|------------------|--------------------------------------------------------------------|
| type       | string           | `"screen_update"`                                                  |
| region_id  | string           | Source region                                                      |
| cursor_row | uint16           | 0-indexed cursor row                                               |
| cursor_col | uint16           | 0-indexed cursor column                                            |
| lines      | []string         | One string per row, space-padded to width, no escape sequences     |
| cells      | [][]ScreenCell   | Optional. Per-cell color/attribute data, same dimensions as lines  |

`lines` is always present for backward compatibility with plain-text consumers. `cells` is present
when the server supports color and provides the full rendering state needed to reconstruct a
colored display.

#### ScreenCell

| Field | Type   | Description                                                                    |
|-------|--------|--------------------------------------------------------------------------------|
| c     | string | Character (omitted if empty/space)                                             |
| fg    | string | Foreground color spec (omitted if default). See Color Spec below.              |
| bg    | string | Background color spec (omitted if default). See Color Spec below.              |
| a     | uint8  | Attribute bitfield (omitted if 0): 1=bold, 2=italic, 4=underline, 8=strikethrough, 16=reverse, 32=blink, 64=conceal |

#### Color Spec

Color specs encode terminal colors as compact strings:

| Format             | Example           | Description                      |
|--------------------|-------------------|----------------------------------|
| ANSI 16 name       | `"red"`, `"brightgreen"` | Standard 16-color palette |
| `5;N`              | `"5;208"`         | 256-color palette (index 0-255)  |
| `2;RRGGBB`         | `"2;ff8700"`      | 24-bit true color (hex RGB)      |

An empty string or absent field means default terminal color.

---

### terminal_events

Sent to subscribed clients when the screen state changes. Contains a batch of terminal operations
captured from the VT parser. The frontend replays these on its local screen to maintain a
synchronized copy.

After the initial `screen_update` on subscribe, all subsequent updates are sent as
`terminal_events` — not as full screen snapshots. This is more efficient since only the changed
operations are transmitted.

```json
{
  "type": "terminal_events",
  "region_id": "abc123",
  "events": [
    {"op": "sgr", "attrs": [1, 31]},
    {"op": "draw", "data": "hello"},
    {"op": "cup", "params": [5, 10]},
    {"op": "el"}
  ]
}
```

| Field     | Type              | Description                            |
|-----------|-------------------|----------------------------------------|
| type      | string            | `"terminal_events"`                    |
| region_id | string            | Source region                          |
| events    | []TerminalEvent   | Ordered batch of terminal operations   |

#### TerminalEvent

Each event describes one terminal operation. Only the fields relevant to the operation are present;
the rest are omitted.

| Field   | Type   | Description                                                                |
|---------|--------|----------------------------------------------------------------------------|
| op      | string | Operation code (see table below)                                           |
| data    | string | Text data (for `draw`, `title`, `icon`, `charset`)                        |
| params  | []int  | Numeric parameters (for cursor moves, modes, margins, etc.)               |
| how     | int    | Erase mode for `ed`/`el` (0=to-end, 1=to-start, 2=all)                   |
| attrs   | []int  | SGR attribute parameters for `sgr`                                        |
| private | bool   | CSI private flag (`?` prefix) for `ed`, `el`, `sgr`, `sm`, `rm`          |

#### Operation codes

| Op       | Description              | Key fields           |
|----------|--------------------------|----------------------|
| draw     | Write text at cursor     | data                 |
| cup      | Cursor position          | params (row, col)    |
| cuu      | Cursor up                | params (count)       |
| cud      | Cursor down              | params (count)       |
| cuf      | Cursor forward           | params (count)       |
| cub      | Cursor back              | params (count)       |
| ed       | Erase in display         | how, private         |
| el       | Erase in line            | how, private         |
| sgr      | Select graphic rendition | attrs, private       |
| sm       | Set mode                 | params, private      |
| rm       | Reset mode               | params, private      |
| lf       | Line feed                |                      |
| cr       | Carriage return          |                      |
| su       | Scroll up                | params (count)       |
| sd       | Scroll down              | params (count)       |
| il       | Insert lines             | params (count)       |
| dl       | Delete lines             | params (count)       |
| ich      | Insert characters        | params (count)       |
| dch      | Delete characters        | params (count)       |
| ech      | Erase characters         | params (count)       |
| decstbm  | Set scroll margins       | params (top, bottom) |
| sc       | Save cursor              |                      |
| rc       | Restore cursor           |                      |
| decsc    | Save cursor (DEC)        |                      |
| decrc    | Restore cursor (DEC)     |                      |
| title    | Set window title         | data                 |
| reset    | Full reset               |                      |
| bell     | Bell                     |                      |

Additional ops exist for less common operations (charset, window ops, rectangle ops, etc.) —
see `frontend/ui/model.go` `replayEvents()` for the complete mapping.

---

### region_destroyed

Sent to clients subscribed to the specific region when its program exits (not broadcast to all clients).

```json
{ "type": "region_destroyed", "region_id": "abc123" }
```

| Field     | Type   | Description           |
|-----------|--------|-----------------------|
| type      | string | `"region_destroyed"`  |
| region_id | string | Destroyed region ID   |

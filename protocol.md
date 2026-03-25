# termd Protocol Specification

Wire format: newline-delimited JSON. Each message is one UTF-8 JSON object followed by `\n`.

All request messages have a corresponding response. The response always includes `error` (bool) and
`message` (string) fields. `message` is empty on success and contains a human-readable description
with context on failure.

Server-initiated messages (`region_created`, `screen_update`, `region_destroyed`) have no response.

---

## Frontend → Server

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

On success, the server writes a `screen_update` with the current screen state to the wire first,
then writes this `subscribe_response`. The frontend will therefore always receive a `screen_update`
before the `subscribe_response` for a given subscription.

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

Sent to subscribed clients when the screen state changes. Contains the full screen as plain text —
no escape sequences, no color, no attributes. libghostty-vt renders all terminal escape sequences
into its internal screen buffer; the server extracts only the visible characters.

```json
{
  "type": "screen_update",
  "region_id": "abc123",
  "cursor_row": 2,
  "cursor_col": 2,
  "lines": [
    "$ echo hello                ",
    "hello                       ",
    "$ _                         "
  ]
}
```

| Field      | Type     | Description                                                     |
|------------|----------|-----------------------------------------------------------------|
| type       | string   | `"screen_update"`                                               |
| region_id  | string   | Source region                                                   |
| cursor_row | uint16   | 0-indexed row of the cursor (0 = top of visible screen)         |
| cursor_col | uint16   | 0-indexed column of the cursor                                  |
| lines      | []string | One string per row, space-padded to width, no escape sequences  |

`len(lines)` equals the current height of the region. Each string is exactly `width` codepoints.

---

### region_destroyed

Sent to all subscribed clients when a region's program exits.

```json
{ "type": "region_destroyed", "region_id": "abc123" }
```

| Field     | Type   | Description           |
|-----------|--------|-----------------------|
| type      | string | `"region_destroyed"`  |
| region_id | string | Destroyed region ID   |

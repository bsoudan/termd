# nxterm Application Specification

## Overview

This document defines how nxterm uses the nxtree protocol. It specifies the tree schema, extensions, gate content types, sink content types, domain-specific patch operations, and RPC methods.

nxterm is a terminal multiplexer. The server manages PTYs and terminal state. Clients connect over the network to view and interact with terminals. The nxtree state tree exposes all server and client state.

## Identity Fields

nxterm extends the `identify` message with application-specific fields:

```json
{
  "type": "identify",
  "capabilities": ["io.nxterm.core/v1", "io.nxterm.stacks/v1"],
  "resume_token": "a8f3b2c1",
  "hostname": "alice-laptop",
  "username": "alice",
  "pid": 12345,
  "process": "nxterm"
}
```

| Field | Type | Description |
|-------|------|-------------|
| hostname | string | Client hostname |
| username | string | Client username |
| pid | int | Client process ID |
| process | string | Client process name |

## Extensions

Each extension owns a top-level key in the tree and may define gate content types, sink content types, custom patch operations, and RPC methods.

| Extension | Tree path | Description |
|-----------|-----------|-------------|
| `io.nxterm.core/v1` | `/core`, `/clients` | Single terminal, client state |
| `io.nxterm.stacks/v1` | `/stacks` | Multiple terminal stacks |
| `io.nxterm.sessions/v1` | `/sessions` | Group stacks into named sessions |
| `io.nxterm.spawn/v1` | (mutates `/stacks`) | Create and destroy stacks |
| `io.nxterm.pty/v1` | (augments stacks) | PTY-specific state and behavior |
| `io.nxterm.programs/v1` | `/programs` | Named program configurations |
| `io.nxterm.admin/v1` | `/admin` | Server introspection |
| `io.nxterm.views/v1` | (augments stacks) | Per-client views, resize policies |
| `io.nxterm.layout/v1` | `/layouts`, (augments `/clients/*/ui`) | Pane layout management |
| `io.nxterm.layers/v1` | (augments stacks) | Multiple layers per stack |
| `io.nxterm.graphics/v1` | (augments layers) | Image/graphics layer content |
| `io.nxterm.clipboard/v1` | `/clipboard` | Clipboard passthrough |
| `io.nxterm.scrollback/v1` | (augments stacks) | Scrollback history access |
| `io.nxterm.upgrade/v1` | `/upgrade` | Live server upgrade |

## Gate Content Types

| Content type | Defined by | Snapshot format | Custom patch ops | Description |
|--------------|------------|-----------------|------------------|-------------|
| `terminal` | `io.nxterm.core/v1` | Screen object | VT terminal operations | Terminal character grid |
| `scrollback` | `io.nxterm.scrollback/v1` | Array of cell rows | `append`, `clear` | Scrollback history |

### `terminal` gate

Snapshot format:

```json
{
  "cursor": {"row": 0, "col": 2, "visible": true},
  "cells": [
    [{"c": "$", "fg": "", "bg": "", "a": 0}, {"c": " "}, ...],
    ...
  ],
  "title": "bash",
  "modes": {"autowrap": true, "cursor_visible": true, ...}
}
```

#### ScreenCell

| Field | Type | Description |
|-------|------|-------------|
| c | string | Character (omitted if space) |
| fg | string | Foreground color spec (omitted if default) |
| bg | string | Background color spec (omitted if default) |
| a | uint8 | Attribute bitfield (omitted if 0): 1=bold, 2=italic, 4=underline, 8=strikethrough, 16=reverse, 32=blink, 64=conceal, 128=faint |

#### Color spec formats

| Format | Example | Description |
|--------|---------|-------------|
| ANSI 16 name | `"red"`, `"brightgreen"` | Standard 16-color palette |
| `5;N` | `"5;208"` | 256-color palette (index 0–255) |
| `2;RRGGBB` | `"2;ff8700"` | 24-bit true color (hex RGB) |

## Sink Content Types

| Content type | Defined by | Payload fields | Description |
|--------------|------------|---------------|-------------|
| `raw_input` | `io.nxterm.pty/v1` | `data` (base64 string) | Raw bytes for a PTY |
| `structured_input` | `io.nxterm.core/v1` | `key`, `modifiers`, `text`, `mouse`, `button`, `col`, `row` | Typed input events for native apps |

### `raw_input` sink

```json
{"type": "send", "path": "/stacks/abc123/input", "data": "bHMgLWxhCg=="}
```

### `structured_input` sink

```json
{"type": "send", "path": "/stacks/abc123/input", "key": "Enter"}
{"type": "send", "path": "/stacks/abc123/input", "key": "c", "modifiers": ["ctrl"]}
{"type": "send", "path": "/stacks/abc123/input", "key": "ArrowUp", "modifiers": ["shift"]}
{"type": "send", "path": "/stacks/abc123/input", "text": "pasted text here"}
{"type": "send", "path": "/stacks/abc123/input", "mouse": "press", "button": 1, "col": 10, "row": 5}
{"type": "send", "path": "/stacks/abc123/input", "mouse": "release", "button": 1, "col": 10, "row": 5}
{"type": "send", "path": "/stacks/abc123/input", "mouse": "move", "col": 12, "row": 5}
```

## Domain-Specific Patch Operations

### Terminal operations

Custom patch ops for gates with content type `terminal`. These correspond to VT100/xterm terminal operations.

| op | Fields | Description |
|----|--------|-------------|
| `draw` | path, row, col, data | Write text at position |
| `cup` | path, row, col | Cursor position (absolute) |
| `cuu` | path, count | Cursor up |
| `cud` | path, count | Cursor down |
| `cuf` | path, count | Cursor forward |
| `cub` | path, count | Cursor back |
| `ed` | path, how, private | Erase in display (0=to end, 1=to start, 2=all) |
| `el` | path, how, private | Erase in line |
| `sgr` | path, attrs, private | Select graphic rendition (colors/attributes) |
| `sm` | path, params, private | Set mode |
| `rm` | path, params, private | Reset mode |
| `lf` | path | Line feed |
| `cr` | path | Carriage return |
| `su` | path, count | Scroll up |
| `sd` | path, count | Scroll down |
| `il` | path, count | Insert lines |
| `dl` | path, count | Delete lines |
| `ich` | path, count | Insert characters |
| `dch` | path, count | Delete characters |
| `ech` | path, count | Erase characters |
| `decstbm` | path, top, bottom | Set scroll margins |
| `sc` | path | Save cursor |
| `rc` | path | Restore cursor |
| `decsc` | path | Save cursor (DEC) |
| `decrc` | path | Restore cursor (DEC) |
| `title` | path, data | Set window title |
| `icon` | path, data | Set icon name |
| `reset` | path | Full terminal reset |
| `bell` | path | Bell |

Example:

```json
{"type": "patch", "patches": [
  {"op": "sgr", "path": "/stacks/abc123/screen", "attrs": [1, 31]},
  {"op": "draw", "path": "/stacks/abc123/screen", "row": 0, "col": 2, "data": "hello"},
  {"op": "cup", "path": "/stacks/abc123/screen", "row": 1, "col": 0}
]}
```

## Tree Schema by Extension

### Core (`io.nxterm.core/v1`)

The minimal extension. One terminal, client state.

```
/core
  /server
    /version: "1.0.0"
    /pid: 1234
    /uptime: 3600
  /terminal
    /screen: {gate: "terminal"}
    /input: {sink: "structured_input"}
    /width: 80
    /height: 24
    /title: "bash"
    /exit_code: null
/clients
  /<client_id>
    /identity: {hostname: "h", username: "u", pid: 123, process: "nxterm"}
    /capabilities: ["io.nxterm.core/v1"]
    /window: {width: 120, height: 40}
    /ui: {}
```

A core-only server runs a single terminal. The client subscribes to `/`, sees `/core/terminal`, subscribes to its screen gate, and sends input to its sink.

Client state at `/clients/<id>/*` is writable by that client. The server populates `/clients/<id>/identity` and `/clients/<id>/capabilities` on connect.

### Stacks (`io.nxterm.stacks/v1`)

Multiple terminal stacks. When present, `/core/terminal` is replaced by `/stacks/*`.

```
/stacks
  /<stack_id>
    /name: "bash"
    /width: 80
    /height: 24
    /title: "bash"
    /exit_code: null
    /screen: {gate: "terminal"}
    /input: {sink: "raw_input"}
```

Client UI state for stacks:

```
/clients/<id>/ui
  /tabs: {order: ["abc123", "def456"], active: "def456"}
  /scroll: {abc123: 0, def456: -50}
```

### Sessions (`io.nxterm.sessions/v1`)

Named groups of stacks.

```
/sessions
  /<name>
    /stacks: ["abc123", "def456"]
```

### Spawn (`io.nxterm.spawn/v1`)

RPC methods for creating and destroying stacks. No tree paths of its own — mutations appear as patches to `/stacks`.

**Methods:**

`io.nxterm.spawn.Spawn`:
```json
→ {"type": "request", "method": "io.nxterm.spawn.Spawn", "params": {"program": "bash", "session": "main"}, "req_id": 1}
← {"type": "response", "req_id": 1, "data": {"stack_id": "abc123"}, "error": ""}
← {"type": "patch", "patches": [{"op": "add", "path": "/stacks/abc123", "value": {...}}]}
```

`io.nxterm.spawn.Kill`:
```json
→ {"type": "request", "method": "io.nxterm.spawn.Kill", "params": {"stack_id": "abc123"}, "req_id": 2}
← {"type": "response", "req_id": 2, "error": ""}
← {"type": "patch", "patches": [{"op": "remove", "path": "/stacks/abc123"}]}
```

### PTY (`io.nxterm.pty/v1`)

Marks stacks as PTY-backed. Adds PTY-specific metadata and changes the input sink type.

```
/stacks/<stack_id>
  /pty: {pid: 1234}
  /input: {sink: "raw_input"}         ← PTY stacks accept raw bytes
```

Stacks without `/pty` are native apps and use `{sink: "structured_input"}`.

### Programs (`io.nxterm.programs/v1`)

Named program configurations.

```
/programs
  /default: {cmd: "/bin/bash", args: []}
  /vim: {cmd: "/usr/bin/vim", args: []}
  /htop: {cmd: "/usr/bin/htop", args: []}
```

**Methods:**

- `io.nxterm.programs.Add(name, cmd, args, env) → {}`
- `io.nxterm.programs.Remove(name) → {}`

### Admin (`io.nxterm.admin/v1`)

Server introspection. Mostly read-only state.

```
/admin
  /num_clients: 2
  /num_stacks: 3
  /listeners: ["unix:/tmp/nxterm.sock", "tcp:localhost:8080"]
```

**Methods:**

- `io.nxterm.admin.KillClient(client_id) → {}`

### Views (`io.nxterm.views/v1`)

Per-client viewing state and resize policies.

```
/stacks/<stack_id>
  /resize_policy: "latest"
  /effective_size: {width: 80, height: 24}
  /viewers
    /42: {width: 120, height: 40}
    /43: {width: 80, height: 24}
```

Resize policies:

| Policy | Behavior |
|--------|----------|
| `latest` | PTY sized to last client that sent a resize. Default. |
| `smallest` | PTY sized to min(all viewer widths) × min(all viewer heights). |
| `largest` | PTY sized to max(all viewer widths) × max(all viewer heights). |
| `fixed` | PTY size set explicitly, not derived from viewers. |

### Layout (`io.nxterm.layout/v1`)

Pane layout state and persistence.

Client-managed layout (in the client's writable subtree):

```
/clients/<id>/ui/layout
  /tree: {
    split: "vertical",
    children: [
      {stack: "abc123", size: 0.6},
      {split: "horizontal", children: [
        {stack: "def456", size: 0.5},
        {stack: "ghi789", size: 0.5}
      ]}
    ]
  }
```

Server-persisted saved layouts:

```
/layouts
  /my-dev-setup: {tree: {...}}
  /monitoring: {tree: {...}}
```

**Methods:**

- `io.nxterm.layout.Save(name) → {}`
- `io.nxterm.layout.Load(name) → {layout}`
- `io.nxterm.layout.List() → {names[]}`

### Layers (`io.nxterm.layers/v1`)

Multiple content layers per stack (grid, graphics, overlay), composited by the client.

```
/stacks/<stack_id>/layers
  /grid
    /screen: {gate: "terminal"}
    /input: {sink: "raw_input"}
  /graphics
    /content: {gate: "graphics"}
  /overlay
    /content: {gate: "overlay"}
```

### Scrollback (`io.nxterm.scrollback/v1`)

Access to scrollback history.

```
/stacks/<stack_id>
  /scrollback: {gate: "scrollback"}
```

### Clipboard (`io.nxterm.clipboard/v1`)

Clipboard passthrough.

```
/clipboard
  /content: {sink: "clipboard_data"}
  /selection: ""
```

### Upgrade (`io.nxterm.upgrade/v1`)

Live server upgrade.

```
/upgrade
  /available: {server: "1.1.0", client: "1.1.0"}
  /status: null
```

**Methods:**

- `io.nxterm.upgrade.Check(client_version, os, arch) → {server_available, client_available}`
- `io.nxterm.upgrade.Start() → {}`

## Example: Full Session

```
# Connect
→ {"type": "identify", "hostname": "alice", "username": "alice", "pid": 100,
   "process": "nxterm", "capabilities": ["io.nxterm.core/v1", "io.nxterm.stacks/v1",
   "io.nxterm.spawn/v1", "io.nxterm.pty/v1"]}
← {"type": "response", "req_id": 0, "client_id": 42, "resume_token": "x7f2", "error": ""}

# Subscribe to root — get the tree (gates closed)
→ {"type": "subscribe", "path": "/", "req_id": 1}
← {"type": "patch", "patches": [{"op": "replace", "path": "/", "value": {
     "core": {"server": {"version": "1.0.0", "pid": 5000}},
     "stacks": {
       "abc123": {"name": "bash", "width": 80, "height": 24, "title": "bash",
                  "exit_code": null, "pty": {"pid": 5001},
                  "screen": {"gate": "terminal"},
                  "input": {"sink": "raw_input"}}
     },
     "clients": {
       "42": {"identity": {"hostname": "alice", "username": "alice"},
              "capabilities": ["io.nxterm.core/v1", "io.nxterm.stacks/v1",
                               "io.nxterm.spawn/v1", "io.nxterm.pty/v1"],
              "window": {"width": 0, "height": 0}, "ui": {}}
     }
   }}]}
← {"type": "response", "req_id": 1, "error": ""}

# Set window size and UI state
→ {"type": "patch", "patches": [
     {"op": "replace", "path": "/clients/42/window", "value": {"width": 120, "height": 40}},
     {"op": "replace", "path": "/clients/42/ui", "value": {"tabs": {"order": ["abc123"], "active": "abc123"}}}
   ]}

# Open the terminal gate — get screen snapshot
→ {"type": "subscribe", "path": "/stacks/abc123/screen", "req_id": 2}
← {"type": "patch", "patches": [{"op": "replace", "path": "/stacks/abc123/screen", "value": {
     "cursor": {"row": 0, "col": 2, "visible": true},
     "cells": [[{"c": "$"}, {"c": " "}]],
     "title": "bash", "modes": {}
   }}]}
← {"type": "response", "req_id": 2, "error": ""}

# Terminal output arrives as patches
← {"type": "patch", "patches": [
     {"op": "draw", "path": "/stacks/abc123/screen", "row": 0, "col": 2, "data": "ls"}
   ]}

# User types
→ {"type": "send", "path": "/stacks/abc123/input", "data": "bHMK"}

# Spawn a new stack
→ {"type": "request", "method": "io.nxterm.spawn.Spawn", "params": {"program": "vim"}, "req_id": 3}
← {"type": "response", "req_id": 3, "data": {"stack_id": "def456"}, "error": ""}
← {"type": "patch", "patches": [{"op": "add", "path": "/stacks/def456", "value": {
     "name": "vim", "width": 80, "height": 24, "title": "vim", "exit_code": null,
     "pty": {"pid": 5002}, "screen": {"gate": "terminal"}, "input": {"sink": "raw_input"}
   }}]}

# Update tabs
→ {"type": "patch", "patches": [
     {"op": "replace", "path": "/clients/42/ui/tabs",
      "value": {"order": ["abc123", "def456"], "active": "def456"}}
   ]}

# Switch subscription to new tab
→ {"type": "unsubscribe", "path": "/stacks/abc123/screen", "req_id": 4}
← {"type": "patch", "patches": [{"op": "replace", "path": "/stacks/abc123/screen", "value": {"gate": "terminal"}}]}
← {"type": "response", "req_id": 4, "error": ""}

→ {"type": "subscribe", "path": "/stacks/def456/screen", "req_id": 5}
← {"type": "patch", "patches": [{"op": "replace", "path": "/stacks/def456/screen", "value": {
     "cursor": {"row": 0, "col": 0, "visible": true}, "cells": [[...]], "title": "vim", "modes": {}
   }}]}
← {"type": "response", "req_id": 5, "error": ""}

# Process exits — appears as patch
← {"type": "patch", "patches": [
     {"op": "replace", "path": "/stacks/def456/exit_code", "value": 0}
   ]}
```

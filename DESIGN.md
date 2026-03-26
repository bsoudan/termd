# termd — Design Document

## Problem

Every terminal multiplexer (tmux, screen, zellij, wezterm) reimplements the same stack: process lifecycle, session persistence, layout, and rendering. The result is no composition, no separation of concerns, and no programmatic access to screen real estate.

A terminal multiplexer is two things fused together: a **session server** and a **frontend**. These should be separate processes speaking a well-defined protocol.

---

## Architecture

The **termd server** is a persistent daemon managing a flat collection of **regions**. A region is a persistent screen surface tied to a running program. The server has no concept of layout, tabs, or spatial arrangement — those are the frontend's concern.

**Frontends** connect to the server, subscribe to regions, and render however they want. Multiple frontends can connect simultaneously. Process lifetime is independent of any frontend connection.

**Programs** run inside regions. They can create new regions, set metadata, and express placement preferences via tags. The server routes screen updates to subscribed frontends and input from frontends back to programs.

---

## Native and Legacy Programs

**Legacy programs** are any existing Unix tool, unmodified. The server spawns every program with a dedicated communication channel. Legacy programs ignore it and write to stdout as normal — the first stdout byte is an unambiguous legacy signal. The server detects this automatically, handles VT parsing, and maintains screen state internally on their behalf. No timeouts or configuration required.

**Native programs** are termd-aware, identify themselves on the communication channel, and own their own screen state. Native program support is deferred to a later milestone.

---

## Regions

A region is the core primitive:
- Unique ID
- Human-readable name
- Bag of arbitrary string key-value **tags**
- Tied to a running program — when the program exits, the region is destroyed. No orphaned regions.

The server manages regions as a flat collection with no inherent hierarchy or layout.

---

## Tags

Tags are the extensibility mechanism. Arbitrary string key-value metadata on every region. The server stores and routes tag changes but never interprets them. Frontends and programs use tags to communicate placement preferences, layout state, capabilities, and application-specific metadata. Namespaced by convention to prevent collisions.

---

## Placement

Programs express placement preferences through tags (beside parent, float, take focus, ephemeral). These are hints — the frontend always has final say. After placing a region, the frontend writes back what it actually did so programs can adapt.

---

## Stack

| Layer | Technology | Rationale |
|---|---|---|
| Server | **Go** | Goroutines per client/region; shared protocol types with frontend; creack/pty for PTY management |
| VT parsing / screen state | **go-te** | Pure Go terminal emulator (port of Python pyte); full screen buffer with cursor tracking; handles escape sequences, resize, alternate buffer |
| Reference frontend | **Go** | Goroutines map naturally onto the frontend's concurrent jobs (socket reader, input forwarder, render loop); bubbletea is a mature TUI framework |
| TUI rendering | **bubbletea + lipgloss** | Elm-style model/update/view architecture; screen updates from server become `tea.Msg`s; lipgloss handles cell styling |

---

## Protocol

### Principles

- **Bubbletea-shaped**: messages are small, discrete, self-describing, and one-way. Designed so a bubbletea frontend requires minimal translation logic, without formally coupling the protocol to Go or bubbletea.
- **Newline-delimited JSON** for v1. Human-readable, debuggable with `nc`, no codegen, no toolchain. Upgrade to a binary format (flatbuffers or msgpack) after message shapes stabilize.
- The protocol spec in `protocol.md` is the canonical source of truth. Both the Zig server and Go frontend derive their types from it.

### Messages

See `protocol.md` for the canonical, up-to-date message definitions. The summary below is intentionally brief; `protocol.md` is the source of truth.

- **Frontend → Server**: `spawn_request`, `subscribe_request`, `input`, `resize_request` (plus their corresponding `_response` messages from the server)
- **Server → Frontend**: `region_created`, `screen_update`, `region_destroyed`

`screen_update` carries a plain-text `lines[]` array (one string per row, no escape sequences) for M1. Colors and attributes are deferred to a later milestone once the protocol stabilizes.

### Future serialization

When the protocol stabilizes, the preferred upgrade path is:
- **Flatbuffers** if a schema + codegen + zero-copy parse is wanted (good Zig story)
- **Msgpack** if compact + fast with no ceremony is sufficient

Do not adopt a binary format until message shapes are stable.

---

## Repository Structure

```
termd/
├── protocol.md              # Canonical protocol spec (source of truth)
├── DESIGN.md                # This document
│
├── server/                  # Zig
│   ├── build.zig
│   ├── build.zig.zon        # libghostty-vt as a Zig module dependency
│   └── src/
│       ├── main.zig         # Entry point, Unix socket listener
│       ├── server.zig       # Top-level state: region registry, client list
│       ├── region.zig       # Region: PTY + libghostty-vt terminal + metadata
│       ├── client.zig       # One per connected frontend, reads/writes protocol
│       └── protocol.zig     # Message types, JSON serialization
│
└── frontend/                # Go
    ├── go.mod
    ├── main.go
    ├── client/
    │   └── client.go        # Socket connection, send/receive, Updates() chan
    ├── protocol/
    │   └── protocol.go      # Mirror of server protocol types
    └── ui/
        ├── model.go         # tea.Model: holds cell grid + region state
        ├── msgs.go          # tea.Msg types + waitForUpdate() cmd
        └── render.go        # View() + renderCell() with lipgloss
```

---

## Milestone 1 — Skeleton

**Goal**: a working server that spawns a legacy program in a region, and a working frontend that connects, subscribes, and renders that region with live I/O.

Deferred to later milestones:
- Native program support
- App manifest
- Tags and placement
- Multiple simultaneous regions
- Protocol diffs / dirty regions
- Binary serialization

**Success criterion**: `bash` (or `vim`, `htop`) runs correctly through the full PTY → libghostty-vt → protocol → bubbletea → render → input → PTY loop.

---

## Key Decisions Log

| Decision | Choice | Rationale |
|---|---|---|
| VT parsing library | go-te | Pure Go port of pyte; full screen buffer with cursor; handles resize and alternate buffer |
| Server language | Go | Goroutines per client/region; shared types with frontend; simpler build |
| Frontend language | Go | Goroutine model fits the frontend's concurrent structure naturally |
| TUI framework | bubbletea + lipgloss | Elm architecture maps well to protocol message flow |
| Wire format (v1) | Newline-delimited JSON | Debuggable; no toolchain; upgrade path clear |
| Wire format (future) | Flatbuffers or msgpack | Decide after protocol stabilizes |
| Layout responsibility | Frontend only | Server has no concept of layout; this is load-bearing for the whole design |

# nxterm OSC Protocol

nxterm uses OSC (Operating System Command) escape sequences for control-plane
communication between test harnesses and the TUI, in addition to the
standard OSC codes handled by the VT100/xterm parser (`pkg/te`).

nxterm's own OSCs use **command number 2459** with a `nx` namespace token
as the first semicolon-delimited payload parameter. Format:

```
ESC ] 2459 ; nx ; <subcommand> ; <payload> ST
```

Where `ST` is either BEL (`\x07`) or the 7-bit form `ESC \`. Both terminators
are accepted by all current parsers.

## Reserved number

nxterm reserves **2459** as its OSC command code. It is not claimed by any
known terminal emulator (XTerm, VTE, iTerm2, Windows Terminal, Kitty,
WezTerm) and was selected to avoid collision with widely-deployed
application codes like 7 (cwd), 8 (hyperlink), 9 (notification), 52
(clipboard), 133 (semantic prompt), 777 (VTE), and 1337 (iTerm2).

The `nx` namespace token provides a second layer of defense: even if
another vendor claims 2459 in the future, their payload format almost
certainly won't start with `nx;` and our parsers will fall through.

## Subcommands

### `sync` — test harness sync barrier (input)

```
ESC ] 2459 ; nx ; sync ; <id> ST
```

Injected onto the TUI's stdin by the test harness. The TUI's rawio
layer strips the sequence from the input stream before any layer sees
it, queues an ack, and emits the matching `ack` OSC on stdout after
the next render cycle.

Use: `nxtest.T.WriteSync(id)` / `nxtest.T.Write(...).Sync(desc)`.

### `ack` — sync barrier acknowledgement (output)

```
ESC ] 2459 ; nx ; ack ; <id> ST
```

Emitted by the TUI to stdout in response to a `sync` request (either
stdin-injected or arriving via a server-side `terminal_events` sync
marker). The TUI writes the ack directly to the program output after
forcing the renderer to flush, so `PtyIO.readLoop` observes the ack
strictly after the rendered frame it signals.

Consumed by: `nxtest.T.WaitSync(id)` scans the PTY output stream for
matching ack markers and releases any waiting `WaitSync` call.

## Parser rules

nxterm OSC sequences are dropped by `pkg/te`'s VT parser — `finishOSC()`
at `internal/server/pkg/te/stream.go` matches known OSC numbers (0, 1,
2, 4, 5, 10, 11, 52, 104, 105, 110–119) and silently discards
everything else. This means ack sequences that leak into a screen cell
render path never affect displayed content.

The TUI's input filter (`rawio.go:isCapabilityResponse`) passes OSC
2459 through as normal input rather than treating it as a terminal
capability response. This allows the sync-in / ack-out pipeline to
work end-to-end without the TUI's bubbletea host consuming the OSC as
part of its terminal-negotiation handshake.

## Adding a new subcommand

1. Reserve a subcommand token (e.g., `status`, `detach`) under the
   `nx` namespace. Pick something short and unambiguous.
2. Define the wire format for request and response payloads. Prefer
   symmetric subcommand names paired with an `-ack` or result form.
3. Add matching constants in `internal/tui/sync.go` (or a new file
   under `internal/tui/` for the new command).
4. Update the input filter if the new sequence flows stdin → TUI.
5. Add unit tests under `internal/tui/` that exercise the parser for
   complete and split input.
6. **Update this document** with the new subcommand spec.

## Changelog

| Version | Change |
|---------|--------|
| v0.1-beta-21 | Reserved OSC 2459 `nx` namespace; added `sync` and `ack` subcommands. |

# Reliability Audit

Date: 2026-04-16

Scope: server (`internal/server/`), TUI client (`internal/tui/`, `internal/client/`, `pkg/layer/`), live upgrade path, protocol, and test coverage.

The audit looks for ways the system can crash, hang, lose data, or corrupt
state — especially in failure paths that aren't exercised by the happy-path
e2e suite.

## Architectural shifts (highest leverage)

### 1. Event loop needs a supervisor
`server.eventLoop()` (`internal/server/server.go:94`) is a single goroutine
that owns all mutable server state. It is started once with no recover, no
restart, and no liveness signal. A panic or deadlock in any request handler
silently wedges the server: the accept loop keeps taking connections but
every dispatch blocks on the `requests` channel.

Wrap the loop in a supervisor that recovers, logs, and either restarts with
a fresh tree or triggers a clean shutdown. Add a liveness heartbeat the
accept loop can probe before admitting new work.

### 2. Two-phase live upgrade with a commit point
`HandleUpgrade` (`internal/server/upgrade.go`) closes listeners, dups PTY
FDs, and freezes the event loop *before* it knows whether the new binary
can actually listen and restore state. Partial failure leaves PTYs
orphaned, listeners closed, and actors half-resumed.

Restructure as **prepare → validate → commit**: the new process binds
listeners on duplicated FDs and ACKs readiness before the old server closes
anything. Only after ACK does the old server flush clients and exit.
Rollback becomes trivial because nothing destructive has happened yet.

### 3. Uniform backpressure contract
Several channels are quietly unbounded or silently drop:

- `Server.requests` — fixed buffer 256, blocking sends from every goroutine
  (`server.go:87`).
- EventProxy sync-mode batch under mode 2026 — no cap
  (`event_proxy.go:30`).
- Actor `msgs` — non-blocking sends that drop without signal
  (`region.go:102`).
- TUI `inbound` — blocking write, no drop counter
  (`internal/client/server.go:246`).

Pick one policy per channel class (bounded + drop-oldest with counter, or
bounded + block + metric), surface it in a shared helper, and assert it in
tests.

### 4. Client-server resync protocol
The TUI reconnect path (`mainlayer.go` drain/reconnect) resumes from
whatever `terminal_events` arrive next — no `GetScreen` on reconnect, no
sequence numbers, no version check. Any event loss during disconnect
silently corrupts the local screen.

Add a per-region sequence number to `terminal_events`. On reconnect,
require a snapshot + replay-from-seq handshake before input resumes. Buffer
raw input in the TUI during disconnect instead of forwarding to a dead
conn.

## Tactical fixes

Priority key:

- **P0** — can crash the server, lose sessions, or leave the system in a
  broken state that requires external restart.
- **P1** — correctness/robustness risk under realistic load or failure.
- **P2** — hardening; unlikely to bite in normal operation but easy to
  exploit accidentally.

### Server

| Pri | Location | Issue | Fix |
|-----|----------|-------|-----|
| P0 | `region.go:549` | `generateUUID` panics on `crypto/rand` failure | Return error; propagate up from spawn path |
| P0 | `region.go:320` | PTY close on spawn error may leave child as zombie | Reap child before closing the PTY |
| P0 | `server.go:94` | Event loop has no supervisor; panic wedges the server silently | Wrap in recover + restart/shutdown policy |
| P1 | `event_proxy.go:30` | Sync mode (DEC 2026) batch unbounded if terminator never arrives | Cap batch; force-exit sync mode on overflow and emit full snapshot |
| P1 | `handlers.go:36` | Malformed JSON is silently dropped | Return an error response so the client doesn't hang |
| P1 | `server.go:87` | `requests` channel blocks when full; no drop policy or metric | Apply the uniform backpressure contract above |
| P2 | `tree.go:69` | `version` is a plain field read outside the event loop | Make atomic or route reads through the loop |
| P2 | `region.go:102` | Non-blocking actor `msgs` sends drop without signal | Surface a drop counter or switch policy |
| P2 | `event_proxy.go:209` | Cursor/status interface assertions silently no-op | Log when screen doesn't satisfy the interface |

### Live upgrade

| Pri | Location | Issue | Fix |
|-----|----------|-------|-----|
| P0 | `upgrade.go:141` | `ptyDups` leaked on send failure | Track dups; close all on rollback |
| P0 | `upgrade.go:321` | `resumeAfterFailedUpgrade` tolerates partial actor resume, leaving nil-channel panics on next input | Fail hard and exit if any actor fails to resume |
| P0 | `upgrade.go:78-109` | Listeners closed before new server validated; rollback cannot re-accept | Keep listeners open until new server ACKs readiness |
| P1 | `upgrade.go:196` | 60s blocking wait on new-process ready; SIGKILL + exit on timeout | Resume old server on timeout rather than exit |
| P1 | `upgrade.go:217` | `flushAndCloseClients` polls `len(writeCh)`; final bytes may not reach wire | Signal flush-complete from writeLoop instead of polling length |
| P1 | `upgrade_recv.go:105` | Region with missing PTY FD is `continue`'d, leaving orphaned session reference | Fail hard or re-create as native region with a clear error |
| P2 | `upgrade_protocol.go:46` | No protocol version on upgrade messages | Add `Protocol` field, validate on receive |

### TUI client

| Pri | Location | Issue | Fix |
|-----|----------|-------|-----|
| P0 | `pkg/layer/stack.go:89` | No recover around layer `Update`; one panic takes down the whole TUI without restoring the terminal | defer/recover, restore terminal, surface error |
| P0 | `pkg/layer/task.go:210` | Task panic logged at Debug only, goroutine silently lost | Elevate log level; propagate to UI; cancel task context |
| P1 | `mainlayer.go` reconnect | In-flight raw input forwarded to dead conn during reconnect; silently lost | Buffer during disconnect; replay (or discard with toast) on reconnect |
| P1 | `terminal.go:210` | Reconnect does not request fresh `GetScreen`; drift from server is silent | Snapshot + seq handshake on reconnect |
| P1 | `internal/client/server.go:246` | `inbound` send is blocking with no drop detection | Non-blocking with drop counter; toast on drops |
| P2 | `rawio.go:216` | stdin reader exit is silent — input stops with no UI feedback | Emit disconnect/error event |
| P2 | `pkg/layer/task.go:336` | Tasks not awaited on shutdown; can leak goroutines | Cancel task contexts in quit path |

## Testing and CI hygiene

- **Race detector is off.** `go test -race` is not run anywhere in the
  Makefile. Add a `test-race` target and run it in CI. The actor + channel
  design will surface real bugs quickly.
- **No upgrade failure tests.** Every failure branch in `upgrade.go` is
  untested. Add: new binary missing, new binary crashes after FDs received,
  partial PTY list, handoff timeout, incompatible protocol version.
- **No chaos / fuzz tests.** Protocol has only hand-written malformed-JSON
  cases. Add `go test -fuzz` for the protocol decoder and for `pkg/te`'s
  escape-sequence parser — both are exposed to untrusted bytes.
- **Stress not in CI.** `make test-stress` is manual-only. Run the 30 s
  variant with `-race` on every push.
- **Flaky `time.Sleep` usage.** ~53 sleep sites in e2e. Replace with
  `WaitFor` predicates tied to observable state (log line, tree version
  bump) so timings are load-independent.

### Concrete tests to add

1. `TestServerCrashDetection` — kill `nxtermd`; verify clients see
   reconnect prompt.
2. `TestPTYChildEOF` — kill child shell; verify region cleanup + tab
   removal.
3. `TestMalformedProtocolRecovery` — truncated JSON, oversized payload,
   invalid UTF-8 color spec; verify no panic or disconnect.
4. `TestNetworkPartition` — slow/stalled TCP reader; verify write-buffer
   exhaustion + drop + reconnect.
5. `TestConcurrentTreeOps` — race tree mutations with `-race`.
6. `TestScrollbackMemoryLeak` — 10 000 lines; measure memory before/after.
7. `TestUpgradeFailure` — missing binary, permission denied, new server
   crashes during handoff.
8. `TestVTParserMalformed` — truncated/corrupted ANSI sequences.
9. `TestStressWithRaceDetector` — existing stress test under `-race`.

## Suggested sequencing

One week of focused work:

1. Wrap the event loop in a supervisor + add a liveness probe.
2. Turn on `-race` in CI; fix what falls out.
3. Refactor upgrade to prepare/validate/commit with FD acknowledgment.

These three remove the top ways this system can brick itself silently.

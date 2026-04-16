# Server concurrency model

This document describes the goroutines, state ownership, and communication
patterns inside `nxtermd`. It is intended as a reference for anyone modifying
the server: which goroutine is allowed to touch which state, what locks
protect what, and where the bottlenecks live.

The high-level rule, from `CLAUDE.md`:

> No mutexes protect server-level maps — all mutations go through
> `Server.requests` channel and are handled in `eventLoop()`.

This document expands on what that means in practice.

---

## Goroutines

The server has roughly five categories of goroutines, distinguished by
lifetime and ownership.

### Long-lived (one per server)

| Goroutine | Started at | Job |
|---|---|---|
| **main** | process start | runs urfave/cli, calls `srv.Run()` which blocks until all accept loops return |
| **signal handler** | `main.go:244` | listens for SIGTERM/SIGINT/SIGUSR2; on SIGUSR2 invokes `srv.HandleUpgrade()`, otherwise `srv.Shutdown()` |
| **event loop** | `server.go:87` (`go s.eventLoop()`) | the heart of the server — sole owner of all server-level state maps; runs in `server_requests.go:209` `eventLoop()` |
| **accept loop × N** | `server.go:108` (one `go func` per listener) | runs `acceptLoop()` per listener; on each accept calls `acceptClient()` which spawns the per-client goroutines |
| **pprof HTTP server** | `pprof.go:24` (optional) | only if `--pprof` flag set |

### Per-client (two per connected client)

| Goroutine | Started at | Job |
|---|---|---|
| **writeLoop** | `client.go:61` (in `NewClient()`, before accept completes) | sole consumer of `c.writeCh`; serializes writes to `c.conn`; emits `dropped_data` warnings when `byteIndex` jumps; closes the conn on exit |
| **ReadLoop** | `server.go:173` (after `addClientReq` ack) | reads newline-delimited JSON from `c.conn`, dispatches each message via `handleMessage()`; **the message handlers run in this goroutine**, which is why they can block on event-loop round trips without deadlocking |

A client lives until either its `closeCh` is closed (`Close()`) or the conn
errors out. The `ReadLoop` defers `removeClientReq` to the event loop, then
closes itself.

### Per-region (three per PTY region)

| Goroutine | Started at | Job |
|---|---|---|
| **readLoop** | `region.go:175` (in `NewRegion()`) | reads from PTY master into a 4KB buffer; runs bytes through `sequenceSafe()` to avoid mid-escape splits; feeds `te.Stream` (which calls into `EventProxy` → mutates `r.screen` and appends to `proxy.batch`); fires non-blocking send on `r.notify`; exits on PTY error or `stopRead` close |
| **waitLoop** | `region.go:176` | calls `cmd.Wait()` (or for restored regions, waits on `readerDone`); on child exit closes `r.notify`, which terminates the watcher's `for range` |
| **watchRegion** | `server.go:200` (after `spawnRegionReq` ack), or `server_requests.go:705` for restored regions | drains `r.notify`; per-tick calls `s.sendTerminalEvents(region)` which flushes `proxy.batch`, asks the event loop for subscribers, and sends `terminal_events`/`screen_update` to each subscriber; after notify is closed, sends a final batch and calls `destroyRegion()` |

### Transient / on-demand

| Goroutine | Started at | Job |
|---|---|---|
| **sessions-changed callback** | `server_requests.go:240` (`go fn(names)`) | dispatched by `notifySessionsChanged()` from inside the event loop after any session create/destroy; runs the user-supplied callback (e.g., mDNS `updateSessions`) so the callback can block without stalling the loop |
| **streamBinary** | `client_upgrade.go:155` | one per `client_binary_request`; reads the file in 64KB chunks and pushes them through `c.sendReply` so the client's `ReadLoop` isn't blocked during the transfer |
| **rollback restart** | `upgrade.go:237` | only on failed upgrade — restarts `readLoop` for regions whose readers had been stopped during the failed handoff |

### Goroutine count, in practice

For a server with 1 listener, 4 connected clients, 2 regions, no upgrades or
downloads in flight: **1 main + 1 signal + 1 event loop + 1 accept + (4×2
client) + (2×3 region)** ≈ **18 goroutines**, plus the Go runtime's own.

---

## The event loop

`eventLoop()` (`server_requests.go:209`) is the only goroutine that touches a
defined set of state maps. It is **strictly sequential** and **must never block
on slow operations** (no PTY forks, no network I/O, no syscalls beyond cheap
maps and channel sends).

It runs a `for { select { case req := <-s.requests: ... ; case <-s.done: ... } }`
loop, type-switching on each request, mutating state under its exclusive
ownership, and sending a response back via the request's `resp` chan.

### State exclusively owned by the event loop

These are **declared as locals inside eventLoop's stack frame**
(`server_requests.go:210-224`), never escape, and are the source of truth:

| Map / variable | Type | Purpose |
|---|---|---|
| `regions` | `map[string]Region` | regionID → Region |
| `clients` | `map[uint32]*Client` | clientID → Client |
| `sessions` | `map[string]*Session` | session name → Session |
| `programs` | `map[string]config.ProgramConfig` | program name → config |
| `subscriptions` | `map[uint32]string` | clientID → currently-subscribed regionID |
| `regionSubs` | `map[string]map[uint32]struct{}` | regionID → set of subscriber clientIDs |
| `clientSessions` | `map[uint32]string` | clientID → currently-attached session name |
| `overlays` | `map[string]*overlayState` | regionID → active overlay (if any) |

Plus, transitively:

- `Session.regions map[string]Region` — also event-loop-owned (mutated only inside `spawnRegionReq` / `destroyRegionReq` / `restoreRegionReq` handlers)
- `overlayState` instances stored in `overlays`

The handoff from `NewServer` → eventLoop is via `init*` fields on the Server
struct (`initRegions`, `initClients`, `initSessions`, `initPrograms`) which the
loop captures and **then nils out** (`server_requests.go:215-219`) to make the
ownership transfer explicit and prevent accidental access from other
goroutines.

### What the event loop does NOT own

- **PTY screen state** — lives inside each `PTYRegion` behind `r.mu`. The event loop calls `region.Snapshot()` and `region.FlushEvents()` from inside handlers, which acquires `r.mu` briefly. (This is the one place the loop acquires a non-trivial lock; it is bounded.)
- **Client `writeCh`** — the loop pushes onto it via `c.SendMessage` from various handlers, but doesn't read from it.
- **The `sessionCreateMus` per-name lock** — held by the *caller* of `findOrCreateSession`, never by the loop itself.

---

## Server struct: shared fields

The `Server` struct (`server.go:18-48`) is the rendezvous point. Everything on
it must either be immutable, atomic, channel-based, or its own self-protected
primitive.

| Field | Synchronization | Notes |
|---|---|---|
| `version, binariesDir, listeners, startTime, sessionsCfg` | immutable after `NewServer` | safe to read from anywhere |
| `nextClientID` | `atomic.Uint32` | incremented in `acceptClient`; also restored from upgrade state |
| `requests` | `chan any` (buffered, 256) | the only path to mutate event-loop state; producers are anything, consumer is the event loop |
| `done` | `chan struct{}` | closed by `Shutdown()` once (gated by `shutdown` CAS); listeners are `s.send`'s fallback case and the event loop's shutdown leg |
| `shutdownResp` | `chan shutdownResult` (buffered, 1) | event loop hands its final state back to `Shutdown()` so the caller can `Close()` clients and regions outside the loop |
| `shutdown` | `atomic.Bool` | CAS gate so `Shutdown()` is idempotent and `acceptLoop()` knows to stop logging errors after listeners close |
| `sessionsChanged` | `atomic.Value` (`func([]string)`) | set once via `SetSessionsChanged`, loaded by `notifySessionsChanged` inside the event loop; callback runs in its own goroutine so it never blocks the loop |
| `sessionCreateMus` | `sync.Map` of `*sync.Mutex` | per-session-name lock to serialize concurrent `findOrCreateSession` calls; held in the *caller's* goroutine across the find-and-spawn pair |
| `initRegions/initClients/initSessions/initPrograms` | only used during construction | nilled by event loop on startup; touching them later is a bug |

---

## Per-region state (`PTYRegion`)

The other place real shared state lives. `PTYRegion` has its own `sync.Mutex`
(`r.mu`) protecting the visible terminal state.

| Field | Protected by | Read by | Written by |
|---|---|---|---|
| `screen` (te.Screen) | `r.mu` | `Snapshot()`, `Resize()` | `readLoop` (via stream→proxy→screen), `Resize()` |
| `proxy.batch` | `r.mu` | `FlushEvents()` | `readLoop` (via stream→proxy) |
| `hscreen` (history) | `r.mu` | `GetScrollback()`, `ScrollbackLen()`, upgrade `MarshalState()` | `readLoop` (transitively) |
| `width, height` | `r.mu` for writes; lock-free reads | `Width()`, `Height()` (no lock — slightly racy but values rarely change and only used for display) | `Resize()` under `r.mu`, set in constructor |
| `id, name, cmd, pid` | immutable after construction | anywhere | constructor only |
| `session` | event loop | event loop, `Session()` getter | `SetSession()` called only from event loop |
| `notify chan struct{}` (buf 1) | itself | `watchRegion` (consumer) | `readLoop` (producer, non-blocking), `waitLoop` (closes) |
| `readerDone chan struct{}` | itself | `watchRegion`, upgrade rollback | closed by `readLoop` |
| `stopRead chan struct{}` | itself | `readLoop` (peek-on-error) | closed by upgrade machinery |
| `savedTermios` | (event loop only) | `RestoreTermios()` | `SaveTermios()` — both called only from event-loop overlay handlers |
| `ptmx *os.File` | none — `os.File`/PTY is itself OS-thread-safe for read/write, but Resize/Snapshot acquire `r.mu` to coordinate | `readLoop` (Read), `WriteInput` (Write), `Resize` (ioctl), `DetachPTY` | constructor |

The interesting thing about region locking: `r.mu` is the **only** lock the
event loop ever acquires (via `Snapshot()` and `FlushEvents()` inside subscribe
and overlay handlers). It's intentionally a tiny critical section so the
loop's worst-case blocking time stays bounded.

---

## Per-client state (`Client`)

| Field | Synchronization | Notes |
|---|---|---|
| `conn net.Conn` | none — only `ReadLoop` reads, only `writeLoop` writes; net.Conn allows concurrent read/write | |
| `writeCh chan writeMsg` (buf 64) | itself | producers: event-loop handlers, `watchRegion` goroutines, `ReadLoop` (via `sendReply`), `streamBinary`. Consumer: `writeLoop` (one). Multiple producers are safe via channel send semantics. |
| `closeCh chan struct{}` | `closeOnce sync.Once` | closed exactly once; selected on by `writeLoop` and the `sendReply` blocking path |
| `identity atomic.Value` (`*clientIdentity`) | itself | written by `handleIdentify` (in ReadLoop goroutine), read by event loop in `getClientInfosReq` and by anyone calling `GetHostname()` etc. |
| `nextByteIndex atomic.Uint64` | itself | each `SendMessage`/`sendReply` allocates a contiguous range of byte indices; `writeLoop` compares against `writtenByteIndex` to detect drops |
| `id, server` | immutable | set in `NewClient` |

The `SendMessage` vs `sendReply` distinction matters:

- **`SendMessage`** is non-blocking (drops on full chan, used for unsolicited pushes like `terminal_events`).
- **`sendReply`** is blocking (used for request/response, called only from this client's own ReadLoop goroutine, which can't deadlock against itself).

---

## Communication patterns

### Path 1: Client request → state mutation

```
client conn ─► ReadLoop (parse JSON, dispatch by type)
                    │
                    ▼
               handleXxx()  ◄── runs in ReadLoop goroutine
                    │
                    ▼ (if state lookup needed)
               s.send(xxxReq{..., resp: ch})
                    │
                    ▼ (event loop processes, sends to ch)
               <-ch
                    │
                    ▼
               reply(...) ─► sendReply ─► writeCh ─► writeLoop ─► conn
```

The ReadLoop is the natural rate limiter — one client can't have more than one
in-flight request at a time (its handler blocks on the event loop response).

### Path 2: PTY output → clients

```
PTY master ─► readLoop (4KB buf)
                  │
                  ▼ r.mu.Lock()
              stream.FeedBytes ─► EventProxy ─► screen + batch
                  │ r.mu.Unlock()
                  ▼
              notify <- {} (non-blocking, drop if full)
                  │
                  ▼
              watchRegion (drains notify)
                  │
                  ▼
              sendTerminalEvents
                  │
                  ▼ r.mu.Lock()
              FlushEvents → events, needsSnapshot
                  │ r.mu.Unlock()
                  │
                  ▼
              s.send(getSubscribersReq) ─► event loop returns []*Client
                  │
                  ▼
              for each client: c.SendMessage(events) ─► writeCh ─► writeLoop ─► conn
```

Two interesting properties of this path:

- **Notify coalescing**: a buffered-1 channel with non-blocking send means burst writes from bash get squashed into one watcher tick, which then drains everything via FlushEvents. This bounds wakeups without losing data (events accumulate in `proxy.batch` regardless of notification).
- **The snapshot ordering rule**: `sendTerminalEvents` flushes events first, *then* asks for subscribers — so events emitted before any subscribers exist are dropped (the screen state survives, since FeedBytes already updated `r.screen`). And the initial snapshot for a new subscriber must be enqueued **inside** the `subscribeReq` event-loop handler (not from `handleSubscribe`) so it can't be passed by an in-flight `sendTerminalEvents` call from another goroutine. See `server_requests.go` `subscribeReq` handler for the comment explaining the ordering.

### Path 3: Region lifecycle

```
Spawn:
  Client.handleSpawn / handleSessionConnect
       │
       ▼
  s.SpawnRegion (in client goroutine)
       │
       ▼
  NewRegion ─► fork+exec, start readLoop, start waitLoop
       │
       ▼
  s.send(spawnRegionReq) ─► event loop registers in maps
       │
       ▼
  go s.watchRegion(region)

Death:
  child exits ─► waitLoop returns ─► close(notify)
       │
       ▼
  watchRegion's for-range exits ─► sendTerminalEvents (final flush)
       │
       ▼
  destroyRegion ─► s.send(destroyRegionReq)
       │
       ▼ event loop unwires:
       - delete from regions, sessions[name].regions, regionSubs, overlays
       - if session is now empty, delete it
       - returns subscriber list
       │
       ▼
  Send region_destroyed to each subscriber
  region.Close() (closes ptmx)
```

---

## Notable invariants and edge cases

1. **The event loop is the bottleneck**, by design. Every state read goes through it. The cost is one goroutine hop and some channel chatter; the benefit is that all server-level state is single-writer, single-reader, no locks needed beyond the per-region screen lock.

2. **The event loop must never block**. It calls into Region methods that take `r.mu` (`Snapshot`, `FlushEvents`), which is bounded — `r.mu` is only held by `readLoop` for the duration of a `FeedBytes` call (microseconds for typical reads). It does NOT do PTY forks, network I/O, or filesystem operations.

3. **PTY forks happen in client goroutines**, not the event loop. `SpawnRegion` calls `NewRegion` (which forks bash, can take milliseconds) in the *caller's* goroutine, then sends a tiny `spawnRegionReq` to the event loop to register. We considered moving this into the event loop and rejected it because the loop blocking on fork+exec would stall every other client.

4. **Snapshot ordering for new subscribers**. When two goroutines (event-loop handler and `watchRegion`) both push to a client's `writeCh`, the order on the wire is whichever push wins the channel send first. The initial snapshot for a newly-subscribing client is therefore pushed from inside the same event-loop step that adds the client to `regionSubs` — this guarantees no `watchRegion` can observe the client as a subscriber until *after* the snapshot is already queued. Pushing the snapshot from `handleSubscribe` (a separate goroutine, after the event loop has already added the client to the subscriber set) would race and could leave the client with terminal_events arriving before its initial screen_update — those events would be dropped by the client (no local screen to apply them to) and the client would be left with a stale empty screen.

5. **`findOrCreateSession` per-name serialization**. Two clients connecting to the same not-yet-existing session would both observe "no session" inside the event-loop handler, then both spawn default programs in their own client goroutines. The per-name `sessionCreateMus` lock serializes the find-and-spawn pair without blocking the event loop. Different session names still run in parallel.

6. **`Region.Snapshot()` and the event loop**. The loop calls this from `subscribeReq` and overlay handlers. This is a deliberate, brief acquisition of `r.mu` (not a violation of the "no locks" rule, but a known cost). Keeping it fast is important: if `readLoop` is mid-FeedBytes for a giant batch, the loop will wait. In practice the batches are small.

7. **`SendMessage` is non-blocking, `sendReply` is blocking**. Watcher goroutines and event-loop handlers use the non-blocking path so they can't be back-pressured by a slow client. Reply paths (always called from the client's own ReadLoop goroutine) use the blocking path so request/response semantics are reliable. Dropped `SendMessage` calls show up as `dropped_data` warnings on the wire because of the `nextByteIndex` accounting.

8. **The `sessionsChanged` callback runs in its own goroutine** (`server_requests.go:240`) precisely so the user-supplied callback (e.g., `discovery.updateSessions` which calls into the mDNS server) can be slow without freezing the event loop.

9. **Live upgrade is a special phase** that breaks normal invariants temporarily: the event loop receives `upgradeReq`, hands its internal maps to the caller, then **parks** (blocks on a nested receive) until either `resumeUpgradeReq` (rollback) or `s.done` (success). During this window, no other requests are processed. The hand-off uses Unix domain socket FD passing to transfer listener and PTY file descriptors to the new process.

10. **Shutdown ordering matters**. `Shutdown()`:
    1. CAS-gates so it's idempotent
    2. closes listeners
    3. closes `done`
    4. waits for the event loop to drain its state into `shutdownResp`
    5. closes clients and regions outside the loop

    The event loop's shutdown path collects its maps into a `shutdownResult` and exits — it does **not** Close anything itself, since closing clients triggers `removeClientReq` sends that would never be received.

# Milestone 13: Replace Shared State with Message Passing

## Context

The termd server uses three mutexes (`Server.mu`, `Client.mu`, `Region.mu`). The `Server.mu` lock is acquired from 19 call sites across multiple goroutines and has already caused a deadlock (commit e0bbf80) when held while calling into `c.mu` on a stuck client. The `Client.mu` lock serializes writes to a network connection with a 5-second deadline, meaning any goroutine calling `SendMessage` can block for 5s holding the lock.

This milestone replaces `Server.mu` with a single-goroutine event loop and replaces `Client.mu` write serialization with a per-client write goroutine. `Region.mu` stays as-is (simple, bounded, no cross-lock risk).

## Implementation Steps

### Step 1: Per-client write goroutine (`client.go`)

Replace `c.mu` write serialization with a channel-based write goroutine.

**Changes to Client struct:**
- Remove `mu sync.Mutex` and `closed bool`
- Add `writeCh chan writeMsg` (buffered, capacity 64)
- Add `closeCh chan struct{}` and `closeOnce sync.Once`
- Add `identity atomic.Value` (stores `*clientIdentity`) for hostname/username/pid/process
- Keep `subscribedRegionID` and `sessionName` with their getters/setters for now (moved in step 3)
  - These need a small `sync.Mutex` temporarily since they're written from ReadLoop and read from other goroutines

**New `writeLoop` goroutine** (started in `NewClient`):
- Selects on `writeCh` and `closeCh`
- Writes raw bytes to `conn` with 5s deadline
- On write error, returns (deferred `conn.Close()` cleans up)
- When `closeCh` fires, returns

**Write channel carries `writeMsg` structs** (not raw bytes):
```go
type writeMsg struct {
    data      []byte
    byteIndex uint64
}
```
The client maintains a `nextByteIndex uint64` counter. Each message sent increments it by `len(data)`. This lets the write goroutine detect gaps when messages are dropped.

**`SendMessage`** (for streaming data — terminal events, broadcasts):
- Marshal to `[]byte`, non-blocking send to `writeCh`
- If channel full, log, increment `nextByteIndex` by the dropped message size, and record the dropped byte count
- The write goroutine notices the gap between its last-written byte index and the incoming `byteIndex`, and sends a `warning` notification to the frontend: `{"type":"warning","warn_type":"dropped_data","message":"lost 1234 bytes"}`

**`sendReply`** (for request/response — client's own ReadLoop is the caller):
- Marshal to `[]byte`, blocking send with `closeCh` escape
- The caller is always the client's own ReadLoop, so blocking is fine
- Still uses `writeMsg` with `byteIndex` for consistency

**`Close()`**: `sync.Once` closes `closeCh`. writeLoop exits, deferred `conn.Close()` fires, ReadLoop's `scanner.Scan()` returns error and exits.

**Identity fields**: `NewClient` stores default `clientIdentity` via `atomic.Value`. `handleIdentify` does `c.identity.Store(...)`. Getters do `c.identity.Load().(*clientIdentity)`.

### Step 2: Server event loop (`server.go` + new `server_requests.go`)

Replace `Server.mu` with a single event loop goroutine that owns all four maps.

**New file `server_requests.go`**: All request/response structs.

Request types (each a struct with optional `resp` channel):

| Request | Fields | Response | Current call site |
|---|---|---|---|
| `addClientReq` | `client *Client` | none (fire & forget) | `acceptClient` |
| `removeClientReq` | `clientID uint32` | none | `removeClient` |
| `spawnRegionReq` | `region *Region, sessionName string` | `resp chan struct{}` | `SpawnRegion` |
| `destroyRegionReq` | `regionID string` | `resp chan destroyResult` (region, subscribers) | `destroyRegion` |
| `findRegionReq` | `regionID string` | `resp chan *Region` | `FindRegion` |
| `broadcastReq` | `msg any` | none | `Broadcast` |
| `killRegionReq` | `regionID string` | `resp chan *Region` | `KillRegion` |
| `killClientReq` | `clientID uint32` | `resp chan *Client` | `KillClient` |
| `getSubscribersReq` | `regionID string` | `resp chan []*Client` | `sendTerminalEvents` |
| `getStatusReq` | | `resp chan statusCounts` | `getStatus` |
| `lookupProgramReq` | `name string` | `resp chan *config.ProgramConfig` | `SpawnProgram` |
| `sessionConnectReq` | `name string` | `resp chan sessionConnectResult` | `findOrCreateSession` |
| `listProgramsReq` | | `resp chan []protocol.ProgramInfo` | `listProgramInfos` |
| `addProgramReq` | `prog config.ProgramConfig` | `resp chan error` | `addProgram` |
| `removeProgramReq` | `name string` | `resp chan error` | `removeProgram` |
| `getRegionInfosReq` | `session string` | `resp chan []protocol.RegionInfo` | `getRegionInfos` |
| `getClientInfosReq` | | `resp chan []clientInfoResult` | `getClientInfos` |
| `getSessionInfosReq` | | `resp chan []protocol.SessionInfo` | `getSessionInfos` |

**Server struct changes:**
- Remove `mu sync.Mutex`, `regions`, `clients`, `sessions`, `programs` fields
- Add `requests chan any` (buffered, capacity 256)
- Add `shutdownResp chan shutdownResult`
- Keep `done chan struct{}` (repurposed as shutdown signal)

**`eventLoop()` goroutine** (started in `NewServer`):
- Owns `regions`, `clients`, `sessions`, `programs` as local variables
- `select` on `s.requests` and `s.done`
- Type-switches on requests and handles each inline
- On `s.done`, sends snapshot of clients/regions to `s.shutdownResp`, returns

**`findOrCreateSession`** becomes two-phase:
1. `sessionConnectReq` checks if session exists. If yes, returns region infos. If no, returns program configs to spawn.
2. Caller spawns each program via `SpawnProgram` (which sends its own requests).
3. Caller sends `getRegionInfosReq` for the final state.

**`Shutdown()`**: closes `s.done` (after listeners). Event loop wakes, snapshots, sends to `shutdownResp`. Shutdown receives and closes clients/regions.

### Step 3: Move subscription + session tracking to event loop

- Remove `subscribedRegionID` and `sessionName` from Client struct
- Remove their getter/setter methods and the temporary mutex from step 1
- Event loop owns `subscriptions map[uint32]string` (clientID → regionID) and `clientSessions map[uint32]string`
- Add `subscribeReq{clientID, regionID, resp}` and `unsubscribeReq{clientID, resp}`
- Add `setClientSessionReq{clientID, sessionName}` (fire & forget)
- `destroyRegionReq` handler clears subscriptions and returns affected clients
- `removeClientReq` handler cleans up subscription and session entries
- `getClientInfosReq` handler reads subscription/session from its own maps

### Step 4: Remove Client.mu entirely

After steps 1 and 3, `Client.mu` is gone — identity uses `atomic.Value`, writes use `writeCh`, subscription/session live in the event loop.

Remove any remaining `sync` import from client.go if unused.

## Files Modified

- `server/client.go` — rewrite: write goroutine, atomic identity, remove mu
- `server/server.go` — rewrite: remove mu, all methods become request senders
- `server/server_requests.go` — **new**: request types, eventLoop, request handlers
- `server/session.go` — no changes (Session is used inside event loop as before)
- `server/region.go` — no changes (Region.mu stays)
- `server/event_proxy.go` — no changes

## Verification

After each step:
1. `make build-server` — compiles
2. `make test-e2e` — all existing e2e tests pass (behavioral equivalence)

Key e2e tests to watch:
- Session connect/reconnect flows (`session_test.go`)
- Spawn/subscribe/input/kill flows (`e2e_test.go`)
- Multi-client scenarios (`termctl_test.go`)
- Program management (`program_test.go`)

Final check: `go vet ./server/...` for any race detector issues. Optionally `go test -race` on e2e.

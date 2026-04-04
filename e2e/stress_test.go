//go:build stress

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// ── Configuration ───────────────────────────────────────────────────────────

type stressConfig struct {
	tuiClients int
	rawClients int
	duration   time.Duration
	seed       int64
}

func loadStressConfig() stressConfig {
	cfg := stressConfig{
		tuiClients: 5,
		rawClients: 3,
		duration:   30 * time.Second,
		seed:       time.Now().UnixNano(),
	}
	if v := os.Getenv("STRESS_TUI_CLIENTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.tuiClients = n
		}
	}
	if v := os.Getenv("STRESS_RAW_CLIENTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.rawClients = n
		}
	}
	if v := os.Getenv("STRESS_DURATION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.duration = d
		}
	}
	if v := os.Getenv("STRESS_SEED"); v != "" {
		if s, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.seed = s
		}
	}
	return cfg
}

// ── Error Collection ────────────────────────────────────────────────────────

type errorCollector struct {
	mu     sync.Mutex
	errors []string
}

func (ec *errorCollector) add(format string, args ...any) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.errors = append(ec.errors, fmt.Sprintf(format, args...))
}

func (ec *errorCollector) report(t *testing.T) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	for _, e := range ec.errors {
		t.Error(e)
	}
}

func (ec *errorCollector) count() int {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	return len(ec.errors)
}

// ── TUI Stress Client ──────────────────────────────────────────────────────

const opTimeout = 3 * time.Second

type tuiClient struct {
	id         int
	name       string
	fe         *frontend
	mu         sync.RWMutex // protects fe during restart; snapshot goroutine takes RLock
	socketPath string
	session    string // "home" session for reconnection after restart
	rng        *rand.Rand
	errs       *errorCollector
	t          *testing.T

	sessionCount int
	opCount      atomic.Int64
	cmdCounter   int
}

var tuiOps = []struct {
	name   string
	weight int
	fn     func(*tuiClient)
}{
	{"type_command", 30, (*tuiClient).opTypeCommand},
	{"spawn_region", 10, (*tuiClient).opSpawnRegion},
	{"switch_tab", 15, (*tuiClient).opSwitchTab},
	{"close_tab", 5, (*tuiClient).opCloseTab},
	{"scrollback", 5, (*tuiClient).opEnterScrollback},
	{"resize", 5, (*tuiClient).opResize},
	{"refresh", 5, (*tuiClient).opRefreshScreen},
	{"status", 5, (*tuiClient).opOpenStatus},
	{"log_viewer", 5, (*tuiClient).opOpenLogViewer},
	{"literal_ctrlb", 5, (*tuiClient).opSendLiteralCtrlB},
	{"create_session", 3, (*tuiClient).opCreateSession},
	{"switch_session", 4, (*tuiClient).opSwitchSession},
	{"kill_session", 3, (*tuiClient).opKillSession},
	{"detach", 2, (*tuiClient).opDetach},
	{"kill_restart", 2, (*tuiClient).opKillRestart},
	{"pause_resume", 3, (*tuiClient).opPauseResume},
}

func (tc *tuiClient) pickOp() (string, func(*tuiClient)) {
	total := 0
	for _, op := range tuiOps {
		total += op.weight
	}
	n := tc.rng.IntN(total)
	for _, op := range tuiOps {
		n -= op.weight
		if n < 0 {
			return op.name, op.fn
		}
	}
	return tuiOps[0].name, tuiOps[0].fn
}

func (tc *tuiClient) tryWaitFor(needle string, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		lines := tc.fe.ScreenLines()
		for _, line := range lines {
			if strings.Contains(line, needle) {
				return true
			}
		}
		select {
		case <-deadline:
			return false
		case _, ok := <-tc.fe.ch:
			if !ok {
				for _, line := range tc.fe.ScreenLines() {
					if strings.Contains(line, needle) {
						return true
					}
				}
				return false
			}
		}
	}
}

// estimateTabCount reads the tab bar (row 0) to count tabs.
// Tabs are labeled "1:name", "2:name", etc.
func (tc *tuiClient) estimateTabCount() int {
	lines := tc.fe.ScreenLines()
	if len(lines) == 0 {
		return 1
	}
	tabBar := lines[0]
	count := 0
	for i := 1; i <= 9; i++ {
		if strings.Contains(tabBar, fmt.Sprintf("%d:", i)) {
			count = i
		}
	}
	if count == 0 {
		return 1
	}
	return count
}

// tryRecover attempts to return the TUI to a normal state after
// a timeout or unexpected condition.
func (tc *tuiClient) tryRecover() {
	for i := 0; i < 3; i++ {
		tc.fe.Write([]byte{0x1b})
		time.Sleep(30 * time.Millisecond)
	}
	tc.fe.Write([]byte("q"))
	tc.fe.Write([]byte{0x03})
	tc.fe.WaitForSilence(300 * time.Millisecond)
}

func (tc *tuiClient) ctrlB(keys ...byte) {
	tc.fe.Write(append([]byte{0x02}, keys...))
}

func (tc *tuiClient) run(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			tc.errs.add("[%s] panic: %v", tc.name, r)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		opName, opFn := tc.pickOp()
		tc.t.Logf("[%s] op #%d: %s", tc.name, tc.opCount.Load(), opName)
		opFn(tc)
		tc.opCount.Add(1)

		d := time.Duration(tc.rng.IntN(250)+50) * time.Millisecond
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		}
	}
}

// tryStartFrontend starts a termd-tui in a PTY, returning an error instead
// of calling t.Fatal. Used for mid-test restarts from goroutines.
func tryStartFrontend(t *testing.T, socketPath, session string) (*frontend, error) {
	args := []string{"--socket", socketPath}
	if session != "" {
		args = append(args, "--session", session)
	}
	cmd := exec.Command("termd-tui", args...)
	cmd.Env = append(testEnv(t), "TERM=dumb")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		return nil, fmt.Errorf("start frontend: %w", err)
	}
	return &frontend{
		ptyIO: newPtyIO(ptmx, 80, 24),
		cmd:   cmd,
		ptmx:  ptmx,
	}, nil
}

func (tc *tuiClient) restart() {
	tc.fe.ptmx.Close()

	fe, err := tryStartFrontend(tc.t, tc.socketPath, tc.session)
	if err != nil {
		tc.errs.add("[%s] restart failed: %v", tc.name, err)
		return
	}

	tc.mu.Lock()
	tc.fe = fe
	tc.mu.Unlock()

	if !tc.tryWaitFor("$", 10*time.Second) {
		tc.errs.add("[%s] restart: prompt never appeared", tc.name)
		tc.tryRecover()
	}
	tc.fe.WaitForSilence(200 * time.Millisecond)
	tc.t.Logf("[%s] restarted", tc.name)
}

// ── TUI Operations ─────────────────────────────────────────────────────────

var shellCommands = []string{
	"echo stress-%d-%d",
	"ls",
	"pwd",
	"date",
	"true",
	"cat /dev/null",
	"seq 1 50",
	"echo",
}

func (tc *tuiClient) opTypeCommand() {
	tc.cmdCounter++
	cmd := shellCommands[tc.rng.IntN(len(shellCommands))]
	if strings.Contains(cmd, "%d") {
		cmd = fmt.Sprintf(cmd, tc.id, tc.cmdCounter)
	}
	tc.fe.Write([]byte(cmd + "\r"))
	tc.fe.WaitForSilence(500 * time.Millisecond)
}

func (tc *tuiClient) opSpawnRegion() {
	if tc.estimateTabCount() >= 9 {
		return
	}
	tc.ctrlB('c')
	if !tc.tryWaitFor("$", opTimeout) {
		tc.errs.add("[%s] spawn_region: timeout waiting for prompt", tc.name)
		tc.tryRecover()
	}
}

func (tc *tuiClient) opSwitchTab() {
	count := tc.estimateTabCount()
	if count <= 1 {
		return
	}
	tab := byte('1') + byte(tc.rng.IntN(count))
	tc.ctrlB(tab)
	tc.fe.WaitForSilence(200 * time.Millisecond)
}

func (tc *tuiClient) opCloseTab() {
	if tc.estimateTabCount() <= 1 {
		return
	}
	tc.ctrlB('x')
	tc.fe.WaitForSilence(500 * time.Millisecond)
}

func (tc *tuiClient) opEnterScrollback() {
	tc.ctrlB('[')
	time.Sleep(200 * time.Millisecond)
	for i := 0; i < tc.rng.IntN(5)+1; i++ {
		tc.fe.Write([]byte{0x1b, '[', 'A'}) // up arrow
		time.Sleep(50 * time.Millisecond)
	}
	tc.fe.Write([]byte("q"))
	tc.fe.WaitForSilence(200 * time.Millisecond)
}

func (tc *tuiClient) opResize() {
	cols := uint16(tc.rng.IntN(81) + 40)
	rows := uint16(tc.rng.IntN(31) + 10)
	tc.fe.Resize(cols, rows)
	tc.fe.WaitForSilence(200 * time.Millisecond)
}

func (tc *tuiClient) opRefreshScreen() {
	tc.ctrlB('r')
	tc.fe.WaitForSilence(200 * time.Millisecond)
}

func (tc *tuiClient) opOpenStatus() {
	tc.ctrlB('s')
	time.Sleep(300 * time.Millisecond)
	tc.fe.Write([]byte{0x1b})
	tc.fe.WaitForSilence(200 * time.Millisecond)
}

func (tc *tuiClient) opOpenLogViewer() {
	tc.ctrlB('l')
	time.Sleep(300 * time.Millisecond)
	tc.fe.Write([]byte{0x1b})
	tc.fe.WaitForSilence(200 * time.Millisecond)
}

func (tc *tuiClient) opSendLiteralCtrlB() {
	tc.ctrlB('b')
	tc.fe.WaitForSilence(200 * time.Millisecond)
}

func (tc *tuiClient) opCreateSession() {
	tc.ctrlB('S', 'o')
	if !tc.tryWaitFor("Session name:", opTimeout) {
		tc.errs.add("[%s] create_session: timeout waiting for name prompt", tc.name)
		tc.tryRecover()
		return
	}
	tc.fe.WaitForSilence(200 * time.Millisecond)
	name := fmt.Sprintf("s%d-%d", tc.id, tc.cmdCounter)
	tc.cmdCounter++
	tc.fe.Write([]byte(name + "\r"))
	if tc.tryWaitFor("$", opTimeout) {
		tc.sessionCount++
	} else {
		tc.errs.add("[%s] create_session: timeout waiting for prompt after naming", tc.name)
		tc.tryRecover()
	}
}

func (tc *tuiClient) opSwitchSession() {
	if tc.sessionCount <= 1 {
		return
	}
	tc.ctrlB('w')
	time.Sleep(300 * time.Millisecond)
	tc.fe.Write([]byte("\r"))
	tc.fe.WaitForSilence(500 * time.Millisecond)
}

func (tc *tuiClient) opKillSession() {
	if tc.sessionCount <= 1 {
		return
	}
	tc.ctrlB('S', 'c')
	tc.fe.WaitForSilence(500 * time.Millisecond)
	tc.sessionCount--
}

func (tc *tuiClient) opDetach() {
	tc.ctrlB('d')
	if err := tc.fe.Wait(5 * time.Second); err != nil {
		tc.errs.add("[%s] detach: %v", tc.name, err)
	}
	tc.restart()
}

func (tc *tuiClient) opKillRestart() {
	tc.fe.cmd.Process.Signal(syscall.SIGINT)
	if err := tc.fe.Wait(5 * time.Second); err != nil {
		tc.errs.add("[%s] kill_restart: %v", tc.name, err)
	}
	tc.restart()
}

// opPauseResume sends SIGSTOP to freeze the TUI so it stops reading from
// the server (exercising the server's write-buffer and drop-detection),
// then SIGCONT to resume.
func (tc *tuiClient) opPauseResume() {
	tc.fe.cmd.Process.Signal(syscall.SIGSTOP)
	d := time.Duration(tc.rng.IntN(2000)+500) * time.Millisecond
	time.Sleep(d)
	tc.fe.cmd.Process.Signal(syscall.SIGCONT)
	tc.fe.WaitForSilence(500 * time.Millisecond)
}

// ── Raw Protocol Stress Client ─────────────────────────────────────────────

const maxRegionsPerRawClient = 10

type rawClient struct {
	id      int
	conn    net.Conn
	rng     *rand.Rand
	errs    *errorCollector
	session string
	t       *testing.T

	mu          sync.Mutex
	regions     []string
	msgCount    map[string]int64
	firstRegion chan struct{}
	opCount     atomic.Int64
}

var rawOps = []struct {
	name   string
	weight int
}{
	{"list_regions", 20},
	{"list_sessions", 15},
	{"list_clients", 15},
	{"status", 15},
	{"spawn_region", 10},
	{"kill_region", 5},
	{"get_screen", 10},
	{"get_scrollback", 5},
	{"send_input", 5},
}

func newRawClient(socketPath string, id int, rng *rand.Rand, errs *errorCollector, t *testing.T) (*rawClient, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	session := fmt.Sprintf("stress-raw-%d", id)
	rc := &rawClient{
		id:          id,
		conn:        conn,
		rng:         rng,
		errs:        errs,
		session:     session,
		t:           t,
		msgCount:    make(map[string]int64),
		firstRegion: make(chan struct{}),
	}
	go rc.readLoop()

	rc.sendJSON(map[string]any{
		"type": "identify", "hostname": "stress", "username": "stress",
		"pid": os.Getpid(), "process": fmt.Sprintf("stress-raw-%d", id),
	})
	rc.sendJSON(map[string]any{
		"type": "session_connect_request", "session": session,
	})
	rc.sendJSON(map[string]any{
		"type": "spawn_request", "session": session, "program": "shell",
	})

	select {
	case <-rc.firstRegion:
	case <-time.After(5 * time.Second):
		conn.Close()
		return nil, fmt.Errorf("timeout waiting for initial region")
	}
	return rc, nil
}

func (rc *rawClient) sendJSON(msg map[string]any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = rc.conn.Write(data)
	return err
}

func (rc *rawClient) readLoop() {
	scanner := bufio.NewScanner(rc.conn)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var msg map[string]any
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			continue
		}
		typ, _ := msg["type"].(string)

		rc.mu.Lock()
		rc.msgCount[typ]++

		switch typ {
		case "spawn_response":
			if errFlag, _ := msg["error"].(bool); !errFlag {
				if id, _ := msg["region_id"].(string); id != "" {
					rc.regions = append(rc.regions, id)
					if len(rc.regions) == 1 {
						select {
						case <-rc.firstRegion:
						default:
							close(rc.firstRegion)
						}
					}
				}
			}
		case "region_destroyed":
			if id, _ := msg["region_id"].(string); id != "" {
				for i, r := range rc.regions {
					if r == id {
						rc.regions = append(rc.regions[:i], rc.regions[i+1:]...)
						break
					}
				}
			}
		}
		rc.mu.Unlock()
	}
}

func (rc *rawClient) close() {
	rc.conn.Close()
}

func (rc *rawClient) randomRegion() string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if len(rc.regions) == 0 {
		return ""
	}
	return rc.regions[rc.rng.IntN(len(rc.regions))]
}

func (rc *rawClient) regionCount() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return len(rc.regions)
}

func (rc *rawClient) run(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			rc.errs.add("[raw-%d] panic: %v", rc.id, r)
		}
	}()

	total := 0
	for _, op := range rawOps {
		total += op.weight
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n := rc.rng.IntN(total)
		var opName string
		for _, op := range rawOps {
			n -= op.weight
			if n < 0 {
				opName = op.name
				break
			}
		}

		switch opName {
		case "list_regions":
			rc.sendJSON(map[string]any{"type": "list_regions_request"})
		case "list_sessions":
			rc.sendJSON(map[string]any{"type": "list_sessions_request"})
		case "list_clients":
			rc.sendJSON(map[string]any{"type": "list_clients_request"})
		case "status":
			rc.sendJSON(map[string]any{"type": "status_request"})
		case "spawn_region":
			if rc.regionCount() < maxRegionsPerRawClient {
				rc.sendJSON(map[string]any{
					"type": "spawn_request", "session": rc.session, "program": "shell",
				})
			}
		case "kill_region":
			if id := rc.randomRegion(); id != "" {
				rc.sendJSON(map[string]any{
					"type": "kill_region_request", "region_id": id,
				})
			}
		case "get_screen":
			if id := rc.randomRegion(); id != "" {
				rc.sendJSON(map[string]any{
					"type": "get_screen_request", "region_id": id,
				})
			}
		case "get_scrollback":
			if id := rc.randomRegion(); id != "" {
				rc.sendJSON(map[string]any{
					"type": "get_scrollback_request", "region_id": id,
				})
			}
		case "send_input":
			if id := rc.randomRegion(); id != "" {
				text := fmt.Sprintf("echo raw-%d-%d\n", rc.id, rc.opCount.Load())
				rc.sendJSON(map[string]any{
					"type": "input", "region_id": id, "data": text,
				})
			}
		}

		rc.opCount.Add(1)

		d := time.Duration(rc.rng.IntN(90)+10) * time.Millisecond
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		}
	}
}

func (rc *rawClient) summary() string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	var parts []string
	for typ, count := range rc.msgCount {
		parts = append(parts, fmt.Sprintf("%s:%d", typ, count))
	}
	return fmt.Sprintf("ops=%d regions=%d msgs=[%s]",
		rc.opCount.Load(), len(rc.regions), strings.Join(parts, " "))
}

// ── Health Monitor ──────────────────────────────────────────────────────────

func healthMonitor(ctx context.Context, t *testing.T, socketPath string, errs *errorCollector) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			out, err := stressRunTermctl(socketPath, "status")
			if err != nil {
				errs.add("[health] termctl status failed: %v — %s", err, strings.TrimSpace(out))
			} else {
				t.Logf("[health] %s", strings.TrimSpace(out))
			}
		}
	}
}

func stressRunTermctl(socketPath string, args ...string) (string, error) {
	fullArgs := append([]string{"--socket", socketPath}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "termctl", fullArgs...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ── Screen Snapshots ────────────────────────────────────────────────────

func snapshotLoop(ctx context.Context, t *testing.T, dir string, clients []*tuiClient) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	seq := 0
	for {
		select {
		case <-ctx.Done():
			// One final snapshot at shutdown
			snapshotAll(t, dir, clients, seq)
			return
		case <-ticker.C:
			snapshotAll(t, dir, clients, seq)
			seq++
		}
	}
}

func snapshotAll(t *testing.T, dir string, clients []*tuiClient, seq int) {
	ts := time.Now().Format("150405")
	for _, tc := range clients {
		tc.mu.RLock()
		lines := tc.fe.ScreenLines()
		tc.mu.RUnlock()
		name := fmt.Sprintf("%s_%03d_%s.txt", tc.name, seq, ts)
		path := filepath.Join(dir, name)
		os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
		t.Logf("[snapshot] %s (%d lines)", path, len(lines))
	}
}

// ── Main Test ───────────────────────────────────────────────────────────────

func TestStress(t *testing.T) {
	cfg := loadStressConfig()
	t.Logf("seed=%d  tui_clients=%d  raw_clients=%d  duration=%v",
		cfg.seed, cfg.tuiClients, cfg.rawClients, cfg.duration)
	t.Logf("reproduce with: STRESS_SEED=%d make test-stress", cfg.seed)

	socketPath, serverCleanup := startServer(t)
	defer serverCleanup()

	snapshotDir, err := os.MkdirTemp("", "stress-snapshots-*")
	if err != nil {
		t.Fatalf("create snapshot dir: %v", err)
	}
	t.Logf("snapshots: %s", snapshotDir)

	errs := &errorCollector{}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.duration)
	defer cancel()

	var wg sync.WaitGroup

	// Health monitor
	wg.Add(1)
	go func() {
		defer wg.Done()
		healthMonitor(ctx, t, socketPath, errs)
	}()

	// Initialize TUI clients: connect, create a dedicated session
	tuiClients := make([]*tuiClient, cfg.tuiClients)
	for i := range cfg.tuiClients {
		rng := rand.New(rand.NewPCG(uint64(cfg.seed), uint64(i)))
		name := fmt.Sprintf("tui-%d", i)

		fe := startFrontendFull(t, socketPath)
		defer fe.Kill()

		sessName := fmt.Sprintf("stress-%d", i)
		tc := &tuiClient{
			id:           i,
			name:         name,
			fe:           fe,
			socketPath:   socketPath,
			session:      sessName,
			rng:          rng,
			errs:         errs,
			t:            t,
			sessionCount: 1,
		}
		tuiClients[i] = tc

		if !tc.tryWaitFor("$", 10*time.Second) {
			t.Fatalf("[%s] initial prompt never appeared", name)
		}
		tc.fe.WaitForSilence(200 * time.Millisecond)

		tc.ctrlB('S', 'o')
		if !tc.tryWaitFor("Session name:", 5*time.Second) {
			t.Fatalf("[%s] session name prompt never appeared", name)
		}
		tc.fe.WaitForSilence(200 * time.Millisecond)
		tc.fe.Write([]byte(sessName + "\r"))
		if !tc.tryWaitFor("$", 10*time.Second) {
			t.Fatalf("[%s] prompt after session create never appeared", name)
		}
		tc.fe.WaitForSilence(200 * time.Millisecond)
		tc.sessionCount = 2 // "main" + the new one

		t.Logf("[%s] ready on session %q", name, sessName)
	}

	// Periodic screen snapshots for manual inspection
	wg.Add(1)
	go func() {
		defer wg.Done()
		snapshotLoop(ctx, t, snapshotDir, tuiClients)
	}()

	// Initialize raw protocol clients
	rawClients := make([]*rawClient, 0, cfg.rawClients)
	for i := range cfg.rawClients {
		rng := rand.New(rand.NewPCG(uint64(cfg.seed), uint64(1000+i)))
		rc, err := newRawClient(socketPath, i, rng, errs, t)
		if err != nil {
			t.Fatalf("[raw-%d] init failed: %v", i, err)
		}
		defer rc.close()
		rawClients = append(rawClients, rc)
		t.Logf("[raw-%d] ready on session %q", i, rc.session)
	}

	// Launch operation loops
	for _, tc := range tuiClients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tc.run(ctx)
		}()
	}
	for _, rc := range rawClients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rc.run(ctx)
		}()
	}

	// Wait for stress duration to elapse
	<-ctx.Done()
	t.Log("stress duration elapsed, stopping clients...")

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("client goroutines did not stop within 10s of context cancellation")
	}

	// Kill current frontends (covers any that were restarted mid-test)
	for _, tc := range tuiClients {
		tc.fe.Kill()
	}

	// Final health check
	out, err := stressRunTermctl(socketPath, "status")
	if err != nil {
		t.Errorf("final health check failed: %v — %s", err, strings.TrimSpace(out))
	} else {
		t.Logf("[final] %s", strings.TrimSpace(out))
	}

	// Stats
	for _, tc := range tuiClients {
		t.Logf("[%s] %d ops completed", tc.name, tc.opCount.Load())
	}
	for _, rc := range rawClients {
		t.Logf("[raw-%d] %s", rc.id, rc.summary())
	}

	if n := errs.count(); n > 0 {
		t.Logf("%d errors collected:", n)
	}
	errs.report(t)
}

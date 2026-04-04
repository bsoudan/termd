package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
	te "nxtermd/pkg/te"
)

// --- Upgrade state types ---

type UpgradeState struct {
	Version       string                       `json:"version"`
	ListenerSpecs []string                     `json:"listener_specs"`
	Sessions      []SessionState               `json:"sessions"`
	Regions       []RegionState                `json:"regions"`
	Programs      map[string]ProgramConfigJSON `json:"programs"`
	SessionsCfg   SessionsCfgJSON              `json:"sessions_cfg"`
	NextClientID  uint32                       `json:"next_client_id"`
	BinariesDir   string                       `json:"binaries_dir,omitempty"`
}

type SessionState struct {
	Name      string   `json:"name"`
	RegionIDs []string `json:"region_ids"`
}

type RegionState struct {
	ID      string           `json:"id"`
	Name    string           `json:"name"`
	Cmd     string           `json:"cmd"`
	Pid     int              `json:"pid"`
	Session string           `json:"session"`
	Width   int              `json:"width"`
	Height  int              `json:"height"`
	Screen  *te.HistoryState `json:"screen"`
}

type ProgramConfigJSON struct {
	Name string            `json:"name"`
	Cmd  string            `json:"cmd"`
	Args []string          `json:"args,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
}

type SessionsCfgJSON struct {
	DefaultName     string   `json:"default_name,omitempty"`
	DefaultPrograms []string `json:"default_programs,omitempty"`
}

// --- Wire protocol ---
//
// Messages are sent as JSON via WriteMsgUnix. When file descriptors
// accompany a message, they are attached as SCM_RIGHTS ancillary data
// on the same sendmsg call, so JSON and FDs always arrive together.

type upgradeMsg struct {
	Type      string          `json:"type"`
	Specs     []string        `json:"specs,omitempty"`
	RegionIDs []string        `json:"region_ids,omitempty"`
	FDCount   int             `json:"fd_count,omitempty"`
	State     json.RawMessage `json:"state,omitempty"`
	Message   string          `json:"message,omitempty"`
}

// sendMsg sends a length-prefixed JSON message, optionally with FDs via SCM_RIGHTS.
// Format: 4-byte big-endian length, then JSON payload. FDs (if any) are attached
// as SCM_RIGHTS ancillary data on the first write.
func sendMsg(conn *net.UnixConn, msg upgradeMsg, fds []int) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	// Length prefix (4 bytes, big-endian).
	length := uint32(len(data))
	header := []byte{byte(length >> 24), byte(length >> 16), byte(length >> 8), byte(length)}

	// Send header + FDs in one sendmsg (FDs attach to the first write).
	var oob []byte
	if len(fds) > 0 {
		oob = unix.UnixRights(fds...)
	}
	if _, _, err := conn.WriteMsgUnix(header, oob, nil); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	// Send payload (may be large, multiple writes ok).
	for len(data) > 0 {
		n, err := conn.Write(data)
		if err != nil {
			return fmt.Errorf("write payload: %w", err)
		}
		data = data[n:]
	}
	return nil
}

// recvMsg receives a length-prefixed JSON message and any accompanying FDs.
func recvMsg(conn *net.UnixConn, timeout time.Duration) (upgradeMsg, []*os.File, error) {
	if timeout > 0 {
		conn.SetReadDeadline(time.Now().Add(timeout))
		defer conn.SetReadDeadline(time.Time{})
	}
	// Read 4-byte header (may carry FDs).
	header := make([]byte, 4)
	oob := make([]byte, 4096)
	n, oobn, _, _, err := conn.ReadMsgUnix(header, oob)
	if err != nil {
		return upgradeMsg{}, nil, fmt.Errorf("read header: %w", err)
	}
	if n < 4 {
		return upgradeMsg{}, nil, fmt.Errorf("short header: %d bytes", n)
	}

	// Parse FDs from header message.
	var files []*os.File
	if oobn > 0 {
		scms, err := unix.ParseSocketControlMessage(oob[:oobn])
		if err != nil {
			return upgradeMsg{}, nil, fmt.Errorf("parse scm: %w", err)
		}
		for _, scm := range scms {
			fds, err := unix.ParseUnixRights(&scm)
			if err != nil {
				return upgradeMsg{}, nil, fmt.Errorf("parse rights: %w", err)
			}
			for _, fd := range fds {
				files = append(files, os.NewFile(uintptr(fd), fmt.Sprintf("fd-%d", fd)))
			}
		}
	}

	// Read payload.
	length := uint32(header[0])<<24 | uint32(header[1])<<16 | uint32(header[2])<<8 | uint32(header[3])
	payload := make([]byte, length)
	read := 0
	for read < int(length) {
		n, err := conn.Read(payload[read:])
		if err != nil {
			return upgradeMsg{}, files, fmt.Errorf("read payload: %w (got %d/%d)", err, read, length)
		}
		read += n
	}

	var msg upgradeMsg
	if err := json.Unmarshal(payload, &msg); err != nil {
		return upgradeMsg{}, files, fmt.Errorf("unmarshal: %w", err)
	}
	return msg, files, nil
}

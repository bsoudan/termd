// nativeapp is a test program that uses the termd overlay protocol.
// It connects to the server socket, registers as an overlay on the
// current region, and renders a cell grid. Input comes from stdin (PTY).
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

type screenCell struct {
	Char string `json:"c,omitempty"`
	Fg   string `json:"fg,omitempty"`
	Bg   string `json:"bg,omitempty"`
	A    uint8  `json:"a,omitempty"`
}

type overlayRegisterRequest struct {
	Type     string `json:"type"`
	RegionID string `json:"region_id"`
}

type overlayRegisterResponse struct {
	Type    string `json:"type"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Error   bool   `json:"error"`
	Message string `json:"message"`
}

type overlayRender struct {
	Type      string         `json:"type"`
	RegionID  string         `json:"region_id"`
	Cells     [][]screenCell `json:"cells"`
	CursorRow uint16         `json:"cursor_row"`
	CursorCol uint16         `json:"cursor_col"`
	Modes     map[int]bool   `json:"modes,omitempty"`
}

type identify struct {
	Type    string `json:"type"`
	Process string `json:"process"`
}

var (
	width    int
	height   int
	regionID string
	input    string
	conn     net.Conn
)

func main() {
	socketPath := os.Getenv("TERMD_SOCKET")
	regionID = os.Getenv("TERMD_REGIONID")
	if socketPath == "" || regionID == "" {
		fmt.Fprintf(os.Stderr, "nativeapp: TERMD_SOCKET and TERMD_REGIONID must be set\n")
		os.Exit(1)
	}

	var err error
	conn, err = net.Dial("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nativeapp: connect: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	// Identify ourselves.
	sendJSON(identify{Type: "identify", Process: "nativeapp"})

	// Register overlay.
	sendJSON(overlayRegisterRequest{Type: "overlay_register", RegionID: regionID})

	// Read response.
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var env struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(scanner.Bytes(), &env) != nil {
			continue
		}
		if env.Type == "overlay_register_response" {
			var resp overlayRegisterResponse
			if json.Unmarshal(scanner.Bytes(), &resp) != nil {
				continue
			}
			if resp.Error {
				fmt.Fprintf(os.Stderr, "nativeapp: register error: %s\n", resp.Message)
				os.Exit(1)
			}
			width = resp.Width
			height = resp.Height
			break
		}
	}

	if width == 0 || height == 0 {
		fmt.Fprintf(os.Stderr, "nativeapp: no register response\n")
		os.Exit(1)
	}

	// Set raw mode to get individual keystrokes.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "nativeapp: raw mode: %v\n", err)
		os.Exit(1)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	render()

	// Handle SIGWINCH for resize.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	go func() {
		for range sigCh {
			render()
		}
	}()

	// Read input from stdin (PTY).
	buf := make([]byte, 256)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			break
		}
		data := buf[:n]
		for _, b := range data {
			if b == 3 { // Ctrl-C
				return
			}
			if b >= 0x20 && b < 0x7f {
				input += string(b)
				render()
			}
		}
	}
}

func render() {
	cells := make([][]screenCell, height)
	for i := range cells {
		cells[i] = make([]screenCell, width)
		// Leave cells empty (transparent) — overlay only draws non-empty cells.
	}

	// Row 0: "NATIVE" in green bold.
	putString(cells, 0, 0, "NATIVE", "green", 1)

	// Row 1: dimensions.
	dims := fmt.Sprintf("%dx%d", width, height)
	putString(cells, 1, 0, dims, "", 0)

	// Row 2: input echo.
	if input != "" {
		putString(cells, 2, 0, "INPUT:"+input, "", 0)
	}

	sendJSON(overlayRender{
		Type:      "overlay_render",
		RegionID:  regionID,
		Cells:     cells,
		CursorRow: 0,
		CursorCol: 0,
	})
}

func putString(cells [][]screenCell, row, col int, s, fg string, attrs uint8) {
	if row >= len(cells) {
		return
	}
	for i, ch := range s {
		c := col + i
		if c >= len(cells[row]) {
			break
		}
		cells[row][c] = screenCell{
			Char: string(ch),
			Fg:   fg,
			A:    attrs,
		}
	}
}

func sendJSON(msg any) {
	b, err := json.Marshal(msg)
	if err != nil {
		return
	}
	b = append(b, '\n')
	conn.Write(b)
}

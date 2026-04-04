// native_scratchpad is a nxtermd native overlay app that provides a drawable canvas.
//
// It connects to the nxtermd server and registers as an overlay on the current
// region. The underlying terminal content shows through transparent cells.
//
// Features:
//   - Move cursor with arrow keys
//   - Type characters at the cursor position in the selected color
//   - Click/drag with the mouse to paint cells
//   - Color palette on the right side — click to select a color
//   - Ctrl-C exits; the overlay is removed and the shell is visible again
//
// Build:
//
//	go build -o native_scratchpad .
//
// Configure in server.toml or run from a shell inside nxtermd:
//
//	./native_scratchpad
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/term"
)

// Protocol types.

type screenCell struct {
	Char string `json:"c,omitempty"`
	Fg   string `json:"fg,omitempty"`
	Bg   string `json:"bg,omitempty"`
	A    uint8  `json:"a,omitempty"`
}

type overlayRender struct {
	Type      string         `json:"type"`
	RegionID  string         `json:"region_id"`
	Cells     [][]screenCell `json:"cells"`
	CursorRow uint16         `json:"cursor_row"`
	CursorCol uint16         `json:"cursor_col"`
	Modes     map[int]bool   `json:"modes,omitempty"`
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

type overlayInput struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

type identify struct {
	Type    string `json:"type"`
	Process string `json:"process"`
}

func privateModeKey(mode int) int { return mode << 5 }

var palette = []struct {
	name string
	spec string
}{
	{"WHT", "white"},
	{"RED", "red"},
	{"GRN", "green"},
	{"YEL", "yellow"},
	{"BLU", "blue"},
	{"MAG", "magenta"},
	{"CYN", "cyan"},
	{"BRD", "brightred"},
	{"BGN", "brightgreen"},
	{"BBL", "brightblue"},
	{"BMG", "brightmagenta"},
	{"BCN", "brightcyan"},
	{"GRY", "brightblack"},
	{"BLK", "black"},
}

const paletteWidth = 5

var (
	width     int
	height    int
	cursorRow int
	cursorCol int
	selColor  int
	canvas    [][]screenCell
	regionID  string
	conn      net.Conn
)

func main() {
	socketPath := os.Getenv("NXTERMD_SOCKET")
	regionID = os.Getenv("NXTERMD_REGIONID")
	if socketPath == "" || regionID == "" {
		fmt.Fprintf(os.Stderr, "native_scratchpad: NXTERMD_SOCKET and NXTERMD_REGIONID must be set\n")
		fmt.Fprintf(os.Stderr, "Run this from a shell inside nxtermd.\n")
		os.Exit(1)
	}

	var err error
	conn, err = net.Dial("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "native_scratchpad: connect: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	sendJSON(identify{Type: "identify", Process: "native_scratchpad"})
	sendJSON(overlayRegisterRequest{Type: "overlay_register", RegionID: regionID})

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	// Read register response.
	for scanner.Scan() {
		var env struct{ Type string `json:"type"` }
		if json.Unmarshal(scanner.Bytes(), &env) != nil {
			continue
		}
		if env.Type == "overlay_register_response" {
			var resp overlayRegisterResponse
			json.Unmarshal(scanner.Bytes(), &resp)
			if resp.Error {
				fmt.Fprintf(os.Stderr, "native_scratchpad: %s\n", resp.Message)
				os.Exit(1)
			}
			width = resp.Width
			height = resp.Height
			break
		}
	}

	initCanvas()
	render()

	// Handle resize via SIGWINCH (the PTY still delivers this).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	go func() {
		for range sigCh {
			w, h, err := term.GetSize(int(os.Stdin.Fd()))
			if err == nil && w > 0 && h > 0 {
				width = w
				height = h
				growCanvas()
				render()
			}
		}
	}()

	// Read input from the server socket (forwarded from TUI clients).
	for scanner.Scan() {
		var env struct{ Type string `json:"type"` }
		if json.Unmarshal(scanner.Bytes(), &env) != nil {
			continue
		}
		if env.Type == "overlay_input" {
			var inp overlayInput
			if json.Unmarshal(scanner.Bytes(), &inp) != nil {
				continue
			}
			decoded, err := base64.StdEncoding.DecodeString(inp.Data)
			if err != nil {
				continue
			}
			handleInput(decoded)
		}
	}
}

func initCanvas() {
	canvas = make([][]screenCell, height)
	for r := range canvas {
		canvas[r] = make([]screenCell, width)
	}
}

func growCanvas() {
	for len(canvas) < height {
		canvas = append(canvas, make([]screenCell, width))
	}
	for r := range canvas {
		for len(canvas[r]) < width {
			canvas[r] = append(canvas[r], screenCell{})
		}
	}
}

func handleInput(data []byte) {
	i := 0
	for i < len(data) {
		// Ctrl-C — exit.
		if data[i] == 3 {
			os.Exit(0)
		}

		if data[i] == 0x1b && i+1 < len(data) && data[i+1] == '[' {
			if i+2 < len(data) && data[i+2] == '<' {
				n := handleSGRMouse(data[i:])
				if n > 0 {
					i += n
					continue
				}
			}
			if i+2 < len(data) {
				switch data[i+2] {
				case 'A':
					if cursorRow > 0 {
						cursorRow--
					}
					render()
					i += 3
					continue
				case 'B':
					if cursorRow < height-1 {
						cursorRow++
					}
					render()
					i += 3
					continue
				case 'C':
					if cursorCol < cw()-1 {
						cursorCol++
					}
					render()
					i += 3
					continue
				case 'D':
					if cursorCol > 0 {
						cursorCol--
					}
					render()
					i += 3
					continue
				}
			}
			i += 2
			continue
		}

		if data[i] == 0x1b {
			i++
			continue
		}

		ch := data[i]
		if ch >= 0x20 && ch < 0x7f {
			if cursorRow < len(canvas) && cursorCol < cw() {
				canvas[cursorRow][cursorCol] = screenCell{
					Char: string(ch),
					Fg:   palette[selColor].spec,
				}
				cursorCol++
				if cursorCol >= cw() {
					cursorCol = cw() - 1
				}
			}
			render()
		}
		i++
	}
}

func handleSGRMouse(data []byte) int {
	if len(data) < 9 {
		return 0
	}
	end := -1
	for j := 3; j < len(data); j++ {
		if data[j] == 'M' || data[j] == 'm' {
			end = j + 1
			break
		}
		if data[j] != ';' && (data[j] < '0' || data[j] > '9') {
			return 0
		}
	}
	if end < 0 {
		return 0
	}

	terminator := data[end-1]
	parts := strings.Split(string(data[3:end-1]), ";")
	if len(parts) != 3 {
		return 0
	}
	btn, e1 := strconv.Atoi(parts[0])
	col, e2 := strconv.Atoi(parts[1])
	row, e3 := strconv.Atoi(parts[2])
	if e1 != nil || e2 != nil || e3 != nil {
		return 0
	}
	col--
	row--

	isPress := terminator == 'M'
	isDrag := btn&32 != 0
	button := btn & 3

	// Paint on left-button click or left-button drag.
	// button==0 is left, button==3 is "no button" (motion only).
	if button == 0 && (isPress || isDrag) {
		paint(row, col)
	}

	return end
}

func paint(row, col int) {
	palStart := width - paletteWidth + 1
	if col >= palStart {
		if row >= 0 && row < len(palette) {
			selColor = row
		}
		render()
		return
	}

	if row >= 0 && row < len(canvas) && col >= 0 && col < cw() {
		canvas[row][col] = screenCell{
			Char: "█",
			Fg:   palette[selColor].spec,
		}
		cursorRow = row
		cursorCol = col
	}
	render()
}

func cw() int {
	w := width - paletteWidth
	if w < 1 {
		return 1
	}
	return w
}

func render() {
	cells := make([][]screenCell, height)
	canvasW := cw()

	for r := 0; r < height; r++ {
		cells[r] = make([]screenCell, width)

		// Canvas area — only draw non-empty cells (empty = transparent).
		for c := 0; c < canvasW && c < width; c++ {
			if r < len(canvas) && c < len(canvas[r]) {
				cells[r][c] = canvas[r][c]
			}
		}

		// Separator.
		sepCol := canvasW
		if sepCol < width {
			cells[r][sepCol] = screenCell{Char: "│", Fg: "brightblack"}
		}

		// Palette.
		palStart := canvasW + 1
		if r < len(palette) {
			label := palette[r].name
			bg := palette[r].spec
			fg := "white"
			if r == selColor {
				fg = "black"
			}
			for c := 0; c < paletteWidth-1 && c < len(label); c++ {
				if palStart+c < width {
					cells[r][palStart+c] = screenCell{Char: string(label[c]), Fg: fg, Bg: bg}
				}
			}
			for c := len(label); c < paletteWidth-1; c++ {
				if palStart+c < width {
					cells[r][palStart+c] = screenCell{Char: " ", Bg: bg}
				}
			}
			if r == selColor {
				if palStart+paletteWidth-1 < width {
					cells[r][palStart+paletteWidth-1] = screenCell{Char: "◄", Fg: palette[r].spec}
				}
			}
		}
	}

	sendJSON(overlayRender{
		Type:      "overlay_render",
		RegionID:  regionID,
		Cells:     cells,
		CursorRow: uint16(cursorRow),
		CursorCol: uint16(cursorCol),
		Modes: map[int]bool{
			privateModeKey(1003): true,
			privateModeKey(1006): true,
		},
	})
}

func sendJSON(msg any) {
	b, err := json.Marshal(msg)
	if err != nil {
		return
	}
	b = append(b, '\n')
	conn.Write(b)
}

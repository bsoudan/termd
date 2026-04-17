// mousehelper enables mouse tracking, reads mouse events from stdin,
// and prints them as plain text. Used by e2e tests to verify mouse passthrough.
package main

import (
	"fmt"
	"os"
	"regexp"
	"strconv"

	"golang.org/x/term"
)

func main() {
	// Put stdin into raw mode to prevent echo
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to set raw mode: %v\n", err)
		os.Exit(1)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	// Enable SGR mouse mode (1002 + 1006). Print a READY marker so
	// tests can synchronize on startup without relying on probe-clicks
	// or sleeps to tell when mouse mode is live.
	fmt.Print("\x1b[?1002h\x1b[?1006h")
	fmt.Print("READY\r\n")
	defer fmt.Print("\x1b[?1002l\x1b[?1006l")

	// SGR mouse pattern: ESC [ < btn ; col ; row M/m
	sgrPattern := regexp.MustCompile(`\x1b\[<(\d+);(\d+);(\d+)([Mm])`)

	buf := make([]byte, 256)
	var accum []byte

	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		accum = append(accum, buf[:n]...)

		// Check for 'q' to quit
		for _, b := range buf[:n] {
			if b == 'q' {
				return
			}
		}

		// Parse any SGR mouse sequences in the accumulated buffer
		for {
			loc := sgrPattern.FindIndex(accum)
			if loc == nil {
				break
			}
			match := sgrPattern.FindSubmatch(accum[loc[0]:loc[1]])
			btn, _ := strconv.Atoi(string(match[1]))
			col, _ := strconv.Atoi(string(match[2]))
			row, _ := strconv.Atoi(string(match[3]))
			kind := "press"
			if string(match[4]) == "m" {
				kind = "release"
			}
			if btn == 64 {
				kind = "wheelup"
			} else if btn == 65 {
				kind = "wheeldown"
			}
			// Use \r\n for raw mode output
			fmt.Printf("MOUSE %s %d %d %d\r\n", kind, btn, col, row)
			accum = accum[loc[1]:]
		}
	}
}

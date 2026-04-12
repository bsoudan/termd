package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"

	"github.com/urfave/cli/v3"
	"nxtermd/internal/config"
	"nxtermd/internal/transport"
)

// base64ChunkSize is the maximum number of base64 characters per
// output line. 4096 stays well under ConPTY's 16384-column width.
const base64ChunkSize = 4096

// proxySentinel is the line printed to stdout once the relay has
// successfully dialed the local nxtermd socket. The optional nonce
// (passed as the second argument by the client) is appended so the
// remote-side scanner cannot accidentally match an sshd login banner.
const proxySentinel = "__NXTERMD_PROXY_READY__"

// cmdProxy implements `nxtermctl proxy [SOCKET] [NONCE]`. It dials a
// local nxtermd unix socket and io.Copys stdin/stdout to/from it, used
// as the remote command in `ssh host -- nxtermctl proxy ...`.
//
// On success it prints the sentinel line to stdout BEFORE any protocol
// bytes flow, so the calling client can detect the boundary between
// ssh authentication chatter and the start of the data stream.
//
// On dial failure it prints a structured error to stderr and exits
// non-zero. The client fishes the stderr message out of its PTY buffer
// and surfaces it in the connect overlay.
func cmdProxy(_ context.Context, cmd *cli.Command) error {
	// Args are [SOCKET] [NONCE] — but when no explicit socket is
	// given the client omits it and NONCE lands in position 0.
	// Distinguish by format: a nonce is a 24-char hex string;
	// a socket path starts with '/' or contains ':'.
	socketArg := cmd.Args().Get(0)
	nonce := cmd.Args().Get(1)
	if socketArg != "" && nonce == "" && isHexNonce(socketArg) {
		nonce = socketArg
		socketArg = ""
	}

	spec, err := resolveProxySocket(socketArg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nxtermctl proxy: %v\n", err)
		os.Exit(2)
	}

	rawConn, err := transport.Dial(spec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nxtermctl proxy: dial %s: %v\n", spec, err)
		os.Exit(3)
	}
	conn := transport.WrapCompression(rawConn)
	defer conn.Close()

	// Sentinel must be on its own line and must precede any protocol
	// bytes from the socket. Write directly to os.Stdout (the file)
	// so it's not buffered behind another goroutine's writes.
	// The sentinel is ALWAYS plain text, even in --base64 mode.
	if nonce != "" {
		fmt.Fprintf(os.Stdout, "%s %s\n", proxySentinel, nonce)
	} else {
		fmt.Fprintln(os.Stdout, proxySentinel)
	}

	if cmd.Bool("base64") {
		return proxyBase64(conn)
	}

	// Raw mode: bidirectional copy.
	errCh := make(chan error, 2)
	go func() { _, err := io.Copy(conn, os.Stdin); errCh <- err }()
	go func() { _, err := io.Copy(os.Stdout, conn); errCh <- err }()
	<-errCh
	return nil
}

// proxyBase64 runs the proxy in base64-encoded mode. Each direction
// encodes/decodes lines using base64 with "." as the end-of-message
// delimiter, making ConPTY byte-mangling irrelevant.
func proxyBase64(conn io.ReadWriteCloser) error {
	errCh := make(chan error, 2)

	// Socket → stdout: read newline-delimited JSON from the socket,
	// base64-encode each line, split into chunks, write "." delimiter.
	go func() {
		scanner := bufio.NewScanner(conn)
		scanner.Buffer(make([]byte, 1<<20), 16<<20)
		w := bufio.NewWriter(os.Stdout)
		for scanner.Scan() {
			line := scanner.Bytes()
			encoded := base64.StdEncoding.EncodeToString(line)
			for len(encoded) > 0 {
				chunk := encoded
				if len(chunk) > base64ChunkSize {
					chunk = encoded[:base64ChunkSize]
				}
				encoded = encoded[len(chunk):]
				w.WriteString(chunk)
				w.WriteByte('\n')
			}
			w.WriteString(".\n")
			w.Flush()
		}
		errCh <- scanner.Err()
	}()

	// Stdin → socket: read lines, accumulate until "." delimiter,
	// join + base64-decode, write the decoded bytes to the socket.
	// The decoded data already contains the \n delimiter from
	// client.Send — don't add another one.
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 1<<20), 16<<20)
		var accum []byte
		for scanner.Scan() {
			line := scanner.Text()
			if line == "." {
				decoded, err := base64.StdEncoding.DecodeString(string(accum))
				if err != nil {
					// Skip malformed input and reset.
					accum = accum[:0]
					continue
				}
				if _, err := conn.Write(decoded); err != nil {
					errCh <- err
					return
				}
				accum = accum[:0]
			} else {
				accum = append(accum, line...)
			}
		}
		errCh <- scanner.Err()
	}()

	<-errCh
	return nil
}

// resolveProxySocket returns a transport.Dial spec for the local
// nxtermd socket. If explicit is non-empty it is used verbatim
// (prefixed with "unix:" if it has no scheme). Otherwise the function
// reads ~/.config/nxtermd/server.toml and picks the first unix listen
// entry, falling back to /tmp/nxtermd.sock.
func resolveProxySocket(explicit string) (string, error) {
	if explicit != "" {
		if !hasScheme(explicit) {
			explicit = "unix:" + explicit
		}
		return explicit, nil
	}

	if cfg, err := config.LoadServerConfig(""); err == nil {
		for _, listen := range cfg.Listen {
			scheme, addr := transport.ParseSpec(listen)
			if scheme != "unix" {
				continue
			}
			if _, err := os.Stat(addr); err == nil {
				return "unix:" + addr, nil
			}
		}
	}

	const fallback = "/tmp/nxtermd.sock"
	if _, err := os.Stat(fallback); err == nil {
		return "unix:" + fallback, nil
	}
	return "", fmt.Errorf("no nxtermd unix socket found (checked server.toml listen entries and %s)", fallback)
}

// hasScheme reports whether spec already has a scheme prefix
// (something like "unix:..." or "tcp:..."). A bare path like "/foo"
// or "./foo" is treated as no scheme.
func hasScheme(spec string) bool {
	if len(spec) == 0 {
		return false
	}
	if spec[0] == '/' || spec[0] == '.' {
		return false
	}
	for i := 0; i < len(spec); i++ {
		if spec[i] == ':' {
			return i > 0
		}
		if spec[i] == '/' {
			return false
		}
	}
	return false
}

// isHexNonce reports whether s looks like a hex nonce (the 24-char
// hex string generated by dialSSHExec) rather than a socket path.
func isHexNonce(s string) bool {
	if len(s) != 24 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

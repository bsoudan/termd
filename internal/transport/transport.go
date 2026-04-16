// Package transport provides multi-transport listener and dialer for nxtermd.
// Address specs have the form "scheme:address":
//
//	unix:/run/user/1000/nxtermd.sock   Unix domain socket
//	tcp:127.0.0.1:9090             TCP
//	ws://host:port/path            WebSocket
//	wss://host:port/path           WebSocket over TLS
//	dssh:user@host:port            Direct SSH (in-process Go SSH client
//	                               talking to nxtermd's own SSH listener)
//	ssh://[user@]host[/sock]       System SSH binary spawning
//	                               `nxtermctl proxy` on the remote
//
// A bare path (starting with / or .) defaults to unix.
package transport

import (
	"fmt"
	"net"
	"os"
	"strings"
)

// Listen creates a net.Listener for the given address spec.
func Listen(spec string) (net.Listener, error) {
	scheme, addr := ParseSpec(spec)
	switch scheme {
	case "unix":
		return listenUnix(addr)
	case "tcp":
		return net.Listen("tcp", addr)
	case "ws":
		return listenWS(addr)
	default:
		return nil, fmt.Errorf("unsupported listener scheme: %q", scheme)
	}
}

// Dial connects to the given address spec and returns a net.Conn.
//
// For interactive transports (currently ssh://) that may need to ask
// the user for credentials or confirmations, use DialWithPrompter
// instead — Dial uses a non-interactive prompter that fails any
// prompt with an error.
func Dial(spec string) (net.Conn, error) {
	return DialWithPrompter(spec, nullPrompter{})
}

// DialWithPrompter is the same as Dial but accepts a Prompter so
// interactive transports can ask the user for passwords, passphrases,
// and host-key confirmations during the dial. Non-interactive
// schemes ignore the prompter.
func DialWithPrompter(spec string, prompter Prompter) (net.Conn, error) {
	scheme, addr := ParseSpec(spec)
	switch scheme {
	case "unix":
		return dialUnix(addr)
	case "tcp":
		return net.Dial("tcp", addr)
	case "ws":
		return dialWS("ws://" + addr)
	case "wss":
		return dialWS("wss://" + addr)
	case "dssh":
		// Direct SSH: in-process Go client → nxtermd's SSH listener.
		// Parse user@host:port from addr.
		user, host := parseSSHAddr(addr)
		return DialSSH(host, user)
	case "ssh":
		// System ssh binary spawned in a PTY → `nxtermctl proxy`
		// on the remote. See ssh_exec.go.
		return dialSSHExec(addr, prompter)
	default:
		return nil, fmt.Errorf("unsupported dial scheme: %q", scheme)
	}
}

// Cleanup performs any necessary cleanup for a listener address (e.g.,
// removing the Unix socket file).
func Cleanup(spec string) {
	scheme, addr := ParseSpec(spec)
	if scheme == "unix" {
		cleanupUnix(addr)
	}
}

// ListenerFile returns the underlying OS file for a listener.
// For SSH and WS listeners, returns the underlying TCP listener's file.
// The caller must close the returned file after use.
func ListenerFile(ln net.Listener) (*os.File, error) {
	switch l := ln.(type) {
	case *net.TCPListener:
		return l.File()
	case *net.UnixListener:
		return l.File()
	case *sshListener:
		if tcpLn, ok := l.tcpLn.(*net.TCPListener); ok {
			return tcpLn.File()
		}
		return nil, fmt.Errorf("ssh listener: underlying is %T, not *net.TCPListener", l.tcpLn)
	case *wsListener:
		if tcpLn, ok := l.tcpLn.(*net.TCPListener); ok {
			return tcpLn.File()
		}
		return nil, fmt.Errorf("ws listener: underlying is %T, not *net.TCPListener", l.tcpLn)
	default:
		return nil, fmt.Errorf("unsupported listener type: %T", ln)
	}
}

// ListenFromFile reconstructs a net.Listener from an OS file and spec.
// The spec determines the listener type (unix, tcp, dssh, ws).
func ListenFromFile(f *os.File, spec string, sshCfg SSHListenerConfig) (net.Listener, error) {
	scheme, _ := ParseSpec(spec)
	ln, err := net.FileListener(f)
	if err != nil {
		return nil, fmt.Errorf("file listener for %s: %w", spec, err)
	}
	switch scheme {
	case "unix", "tcp":
		return ln, nil
	case "dssh":
		return ListenSSHFromListener(ln, sshCfg)
	case "ws":
		return listenWSFromListener(ln)
	default:
		ln.Close()
		return nil, fmt.Errorf("unsupported scheme for file listener: %q", scheme)
	}
}

func parseSSHAddr(addr string) (user, host string) {
	if at := strings.Index(addr, "@"); at >= 0 {
		return addr[:at], addr[at+1:]
	}
	return "", addr
}

// ParseSpec splits an address spec into its scheme and address. Bare paths
// (starting with / or .) and spec with no scheme default to unix. The
// optional "//" separator after the scheme is stripped, so
// "tcp://host:port" and "tcp:host:port" both return ("tcp", "host:port").
func ParseSpec(spec string) (scheme, addr string) {
	// Bare paths default to unix
	if strings.HasPrefix(spec, "/") || strings.HasPrefix(spec, ".") {
		return "unix", spec
	}
	if i := strings.Index(spec, ":"); i > 0 {
		scheme = spec[:i]
		addr = spec[i+1:]
		// Strip leading "//" from URL-style specs (tcp://host:port)
		addr = strings.TrimPrefix(addr, "//")
		return scheme, addr
	}
	// No scheme prefix, assume unix
	return "unix", spec
}

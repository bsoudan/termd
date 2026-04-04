// Package transport provides multi-transport listener and dialer for nxtermd.
// Address specs have the form "scheme:address":
//
//	unix:/tmp/nxtermd.sock   Unix domain socket
//	tcp:127.0.0.1:9090     TCP
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
	scheme, addr := parseSpec(spec)
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
func Dial(spec string) (net.Conn, error) {
	scheme, addr := parseSpec(spec)
	switch scheme {
	case "unix":
		return dialUnix(addr)
	case "tcp":
		return net.Dial("tcp", addr)
	case "ws":
		return dialWS("ws://" + addr)
	case "wss":
		return dialWS("wss://" + addr)
	case "ssh":
		// Parse user@host:port from addr
		user, host := parseSSHAddr(addr)
		return DialSSH(host, user)
	default:
		return nil, fmt.Errorf("unsupported dial scheme: %q", scheme)
	}
}

// Cleanup performs any necessary cleanup for a listener address (e.g.,
// removing the Unix socket file).
func Cleanup(spec string) {
	scheme, addr := parseSpec(spec)
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
// The spec determines the listener type (unix, tcp, ssh, ws).
func ListenFromFile(f *os.File, spec string, sshCfg SSHListenerConfig) (net.Listener, error) {
	scheme, _ := parseSpec(spec)
	ln, err := net.FileListener(f)
	if err != nil {
		return nil, fmt.Errorf("file listener for %s: %w", spec, err)
	}
	switch scheme {
	case "unix", "tcp":
		return ln, nil
	case "ssh":
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

func parseSpec(spec string) (scheme, addr string) {
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

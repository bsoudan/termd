// Package transport provides multi-transport listener and dialer for termd.
// Address specs have the form "scheme:address":
//
//	unix:/tmp/termd.sock   Unix domain socket
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
		os.Remove(addr)
		return net.Listen("unix", addr)
	case "tcp":
		return net.Listen("tcp", addr)
	default:
		return nil, fmt.Errorf("unsupported listener scheme: %q", scheme)
	}
}

// Dial connects to the given address spec and returns a net.Conn.
func Dial(spec string) (net.Conn, error) {
	scheme, addr := parseSpec(spec)
	switch scheme {
	case "unix":
		return net.Dial("unix", addr)
	case "tcp":
		return net.Dial("tcp", addr)
	default:
		return nil, fmt.Errorf("unsupported dial scheme: %q", scheme)
	}
}

// Cleanup performs any necessary cleanup for a listener address (e.g.,
// removing the Unix socket file).
func Cleanup(spec string) {
	scheme, addr := parseSpec(spec)
	if scheme == "unix" {
		os.Remove(addr)
	}
}

func parseSpec(spec string) (scheme, addr string) {
	// Bare paths default to unix
	if strings.HasPrefix(spec, "/") || strings.HasPrefix(spec, ".") {
		return "unix", spec
	}
	if i := strings.Index(spec, ":"); i > 0 {
		return spec[:i], spec[i+1:]
	}
	// No scheme prefix, assume unix
	return "unix", spec
}

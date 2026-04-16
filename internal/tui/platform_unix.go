//go:build !windows

package tui

import "nxtermd/internal/config"

var defaultSocket = config.DefaultSocket()

// inferEndpoint returns the endpoint as-is on Unix. The transport
// package's parseSpec handles bare paths (defaulting to unix:).
func inferEndpoint(endpoint string) string {
	return endpoint
}

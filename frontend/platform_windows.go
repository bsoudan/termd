//go:build windows

package main

// No default socket on Windows — user must specify a transport explicitly.
const defaultSocket = ""

// inferEndpoint returns the endpoint as-is on Windows. Unix sockets are
// not supported; users must provide a scheme (tcp:, ws:, ssh:).
func inferEndpoint(endpoint string) string {
	return endpoint
}

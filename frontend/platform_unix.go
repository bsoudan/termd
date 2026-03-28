//go:build !windows

package main

const defaultSocket = "/tmp/termd.sock"

// inferEndpoint returns the endpoint as-is on Unix. The transport
// package's parseSpec handles bare paths (defaulting to unix:).
func inferEndpoint(endpoint string) string {
	return endpoint
}

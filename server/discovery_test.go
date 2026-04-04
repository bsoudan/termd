package main

import "testing"

func TestTransportScheme(t *testing.T) {
	tests := []struct {
		spec string
		want string
	}{
		{"tcp://0.0.0.0:9100", "tcp"},
		{"tcp:0.0.0.0:9100", "tcp"},
		{"ssh://0.0.0.0:2222", "ssh"},
		{"ws://0.0.0.0:8080", "ws"},
		{"unix:/tmp/nxtermd.sock", "unix"},
	}
	for _, tt := range tests {
		got := transportScheme(tt.spec)
		if got != tt.want {
			t.Errorf("transportScheme(%q) = %q, want %q", tt.spec, got, tt.want)
		}
	}
}

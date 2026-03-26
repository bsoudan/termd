package transport

import (
	"io"
	"net"
	"path/filepath"
	"testing"
)

func TestParseSpec(t *testing.T) {
	tests := []struct {
		spec       string
		wantScheme string
		wantAddr   string
	}{
		{"/tmp/termd.sock", "unix", "/tmp/termd.sock"},
		{"./termd.sock", "unix", "./termd.sock"},
		{"unix:/tmp/termd.sock", "unix", "/tmp/termd.sock"},
		{"tcp:127.0.0.1:9090", "tcp", "127.0.0.1:9090"},
		{"tcp:0.0.0.0:0", "tcp", "0.0.0.0:0"},
	}
	for _, tt := range tests {
		scheme, addr := parseSpec(tt.spec)
		if scheme != tt.wantScheme || addr != tt.wantAddr {
			t.Errorf("parseSpec(%q) = (%q, %q), want (%q, %q)",
				tt.spec, scheme, addr, tt.wantScheme, tt.wantAddr)
		}
	}
}

func TestUnixRoundTrip(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "test.sock")
	ln, err := Listen("unix:" + sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	roundTrip(t, ln, "unix:"+sock)
}

func TestTCPRoundTrip(t *testing.T) {
	ln, err := Listen("tcp:127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr)
	spec := "tcp:" + addr.String()
	roundTrip(t, ln, spec)
}

func roundTrip(t *testing.T, ln net.Listener, dialSpec string) {
	t.Helper()

	done := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		buf := make([]byte, 64)
		n, _ := conn.Read(buf)
		done <- string(buf[:n])
	}()

	conn, err := Dial(dialSpec)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	_, err = conn.Write([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()

	got := <-done
	if got != "hello" {
		t.Errorf("round-trip got %q, want %q", got, "hello")
	}
}

func TestCleanup(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "cleanup.sock")
	ln, err := Listen("unix:" + sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()

	// Socket file should exist
	Cleanup("unix:" + sock)

	// Now it should be gone
	_, err = net.Dial("unix", sock)
	if err == nil {
		t.Fatal("expected dial to fail after cleanup")
	}
}

func TestDialUnknownScheme(t *testing.T) {
	_, err := Dial("ftp:host:21")
	if err == nil {
		t.Fatal("expected error for unknown scheme")
	}
}

func TestListenUnknownScheme(t *testing.T) {
	_, err := Listen("ftp:host:21")
	if err == nil {
		t.Fatal("expected error for unknown scheme")
	}
}

// bidirectional verifies data flows both ways.
func TestTCPBidirectional(t *testing.T) {
	ln, err := Listen("tcp:127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(conn, conn) // echo server
	}()

	conn, err := Dial("tcp:" + addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	_, err = conn.Write([]byte("ping"))
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 4)
	_, err = io.ReadFull(conn, buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Errorf("echo got %q, want %q", string(buf), "ping")
	}
}

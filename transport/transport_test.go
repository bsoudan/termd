package transport

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestParseSpec(t *testing.T) {
	tests := []struct {
		spec       string
		wantScheme string
		wantAddr   string
	}{
		{"/tmp/nxtermd.sock", "unix", "/tmp/nxtermd.sock"},
		{"./nxtermd.sock", "unix", "./nxtermd.sock"},
		{"unix:/tmp/nxtermd.sock", "unix", "/tmp/nxtermd.sock"},
		{"tcp://127.0.0.1:9090", "tcp", "127.0.0.1:9090"},
		{"tcp://0.0.0.0:0", "tcp", "0.0.0.0:0"},
		{"unix:///tmp/nxtermd.sock", "unix", "/tmp/nxtermd.sock"},
		{"ws://127.0.0.1:8080", "ws", "127.0.0.1:8080"},
		{"wss://host:443/ws", "wss", "host:443/ws"},
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
	ln, err := Listen("tcp://127.0.0.1:0")
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

func TestWSRoundTrip(t *testing.T) {
	ln, err := Listen("ws://127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr)
	spec := "ws://127.0.0.1:" + fmt.Sprintf("%d", addr.Port)
	roundTrip(t, ln, spec)
}

func TestSSHRoundTrip(t *testing.T) {
	dir := t.TempDir()
	hostKeyPath := filepath.Join(dir, "host_key")
	clientKeyPath := filepath.Join(dir, "client_key")
	authKeysPath := filepath.Join(dir, "authorized_keys")

	// Generate client key
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privPEM, err := ssh.MarshalPrivateKey(privKey, "")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(clientKeyPath, pem.EncodeToMemory(privPEM), 0600)

	// Write authorized_keys
	sshPub, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(authKeysPath, ssh.MarshalAuthorizedKey(sshPub), 0644)

	// Start SSH listener (host key will be auto-generated)
	ln, err := ListenSSH("127.0.0.1:0", SSHListenerConfig{
		HostKeyPath:        hostKeyPath,
		AuthorizedKeysPath: authKeysPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr)

	// Dial with the client key
	signer, err := ssh.ParsePrivateKey(pem.EncodeToMemory(privPEM))
	if err != nil {
		t.Fatal(err)
	}
	clientConfig := &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

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

	sshConn, err := ssh.Dial("tcp", addr.String(), clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	ch, _, err := sshConn.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	ch.Write([]byte("hello"))
	ch.Close()
	sshConn.Close()

	got := <-done
	if got != "hello" {
		t.Errorf("round-trip got %q, want %q", got, "hello")
	}
}

func TestSSHAuthFailure(t *testing.T) {
	dir := t.TempDir()
	hostKeyPath := filepath.Join(dir, "host_key")
	authKeysPath := filepath.Join(dir, "authorized_keys")

	// Write an authorized_keys with a different key than what the client uses
	_, goodPriv, _ := ed25519.GenerateKey(rand.Reader)
	goodPub, _ := ssh.NewPublicKey(goodPriv.Public())
	os.WriteFile(authKeysPath, ssh.MarshalAuthorizedKey(goodPub), 0644)

	ln, err := ListenSSH("127.0.0.1:0", SSHListenerConfig{
		HostKeyPath:        hostKeyPath,
		AuthorizedKeysPath: authKeysPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr)

	// Generate a different client key (not authorized)
	_, badPriv, _ := ed25519.GenerateKey(rand.Reader)
	badSigner, _ := ssh.NewSignerFromKey(badPriv)

	clientConfig := &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(badSigner)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	_, err = ssh.Dial("tcp", addr.String(), clientConfig)
	if err == nil {
		t.Fatal("expected auth failure, got success")
	}
}

// bidirectional verifies data flows both ways.
func TestTCPBidirectional(t *testing.T) {
	ln, err := Listen("tcp://127.0.0.1:0")
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

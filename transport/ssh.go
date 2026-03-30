package transport

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// SSHListenerConfig configures the SSH transport listener.
type SSHListenerConfig struct {
	// HostKeyPath is the path to the SSH host key. If empty or missing,
	// a new ed25519 key is generated and written to this path.
	HostKeyPath string

	// AuthorizedKeysPath is the path to an authorized_keys file.
	// Defaults to ~/.ssh/authorized_keys if empty.
	AuthorizedKeysPath string

	// NoAuth disables authentication entirely. Must be explicitly set.
	NoAuth bool
}

// ListenSSH starts an SSH server on addr and returns a net.Listener.
// Each authenticated SSH session channel is wrapped as a net.Conn.
func ListenSSH(addr string, cfg SSHListenerConfig) (net.Listener, error) {
	serverConfig := &ssh.ServerConfig{}

	// Load or generate host key
	hostKey, err := loadOrGenerateHostKey(cfg.HostKeyPath)
	if err != nil {
		return nil, fmt.Errorf("ssh host key: %w", err)
	}
	serverConfig.AddHostKey(hostKey)

	// Auth
	if cfg.NoAuth {
		slog.Warn("ssh: authentication disabled (--ssh-no-auth)")
		serverConfig.NoClientAuth = true
	} else {
		authKeysPath := cfg.AuthorizedKeysPath
		if authKeysPath == "" {
			home, _ := os.UserHomeDir()
			if home != "" {
				authKeysPath = home + "/.ssh/authorized_keys"
			}
		}
		authorizedKeys, err := loadAuthorizedKeys(authKeysPath)
		if err != nil {
			return nil, fmt.Errorf("ssh authorized keys: %w", err)
		}
		if authorizedKeys == nil || len(authorizedKeys) == 0 {
			return nil, fmt.Errorf("ssh: no authorized keys found at %s (use --ssh-auth-keys or --ssh-no-auth)", authKeysPath)
		}
		slog.Info("ssh: loaded authorized keys", "path", authKeysPath, "keys", len(authorizedKeys))
		serverConfig.PublicKeyCallback = func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if _, ok := authorizedKeys[string(key.Marshal())]; ok {
				return nil, nil
			}
			return nil, fmt.Errorf("unknown public key for %s", conn.User())
		}
	}

	tcpLn, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("ssh listen: %w", err)
	}

	return listenSSHFromListener(tcpLn, serverConfig)
}

// ListenSSHFromListener creates an SSH listener from an existing TCP listener.
func ListenSSHFromListener(tcpLn net.Listener, cfg SSHListenerConfig) (net.Listener, error) {
	serverConfig := &ssh.ServerConfig{}
	hostKey, err := loadOrGenerateHostKey(cfg.HostKeyPath)
	if err != nil {
		return nil, fmt.Errorf("ssh host key: %w", err)
	}
	serverConfig.AddHostKey(hostKey)

	if cfg.NoAuth {
		serverConfig.NoClientAuth = true
	} else {
		authKeysPath := cfg.AuthorizedKeysPath
		if authKeysPath == "" {
			home, _ := os.UserHomeDir()
			if home != "" {
				authKeysPath = home + "/.ssh/authorized_keys"
			}
		}
		authorizedKeys, err := loadAuthorizedKeys(authKeysPath)
		if err != nil {
			return nil, fmt.Errorf("ssh authorized keys: %w", err)
		}
		if len(authorizedKeys) == 0 {
			return nil, fmt.Errorf("ssh: no authorized keys found at %s", authKeysPath)
		}
		serverConfig.PublicKeyCallback = func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if _, ok := authorizedKeys[string(key.Marshal())]; ok {
				return nil, nil
			}
			return nil, fmt.Errorf("unknown public key for %s", conn.User())
		}
	}

	return listenSSHFromListener(tcpLn, serverConfig)
}

func listenSSHFromListener(tcpLn net.Listener, serverConfig *ssh.ServerConfig) (net.Listener, error) {
	sl := &sshListener{
		tcpLn:  tcpLn,
		config: serverConfig,
		conns:  make(chan net.Conn, 16),
		done:   make(chan struct{}),
	}
	go sl.acceptLoop()
	return sl, nil
}

type sshListener struct {
	tcpLn  net.Listener
	config *ssh.ServerConfig
	conns  chan net.Conn
	done   chan struct{}
	closeOnce sync.Once
}

func (sl *sshListener) acceptLoop() {
	for {
		tcpConn, err := sl.tcpLn.Accept()
		if err != nil {
			select {
			case <-sl.done:
				return
			default:
				slog.Debug("ssh accept error", "error", err)
				continue
			}
		}
		go sl.handleConn(tcpConn)
	}
}

func (sl *sshListener) handleConn(tcpConn net.Conn) {
	sshConn, chans, reqs, err := ssh.NewServerConn(tcpConn, sl.config)
	if err != nil {
		slog.Debug("ssh handshake failed", "error", err)
		tcpConn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			newCh.Reject(ssh.UnknownChannelType, "unsupported channel type")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			slog.Debug("ssh channel accept error", "error", err)
			continue
		}
		// Discard session requests (shell, exec, etc.)
		go ssh.DiscardRequests(chReqs)

		nc := newSSHConn(ch, sshConn)
		select {
		case sl.conns <- nc:
		case <-sl.done:
			ch.Close()
			return
		}
	}
}

func (sl *sshListener) Accept() (net.Conn, error) {
	select {
	case conn := <-sl.conns:
		return conn, nil
	case <-sl.done:
		return nil, net.ErrClosed
	}
}

func (sl *sshListener) Close() error {
	var err error
	sl.closeOnce.Do(func() {
		close(sl.done)
		err = sl.tcpLn.Close()
	})
	return err
}

func (sl *sshListener) Addr() net.Addr {
	return sl.tcpLn.Addr()
}

// sshConn wraps an ssh.Channel as a net.Conn.
type sshConn struct {
	ssh.Channel
	remoteAddr net.Addr
	localAddr  net.Addr
}

func newSSHConn(ch ssh.Channel, sconn *ssh.ServerConn) *sshConn {
	return &sshConn{
		Channel:    ch,
		remoteAddr: sconn.RemoteAddr(),
		localAddr:  sconn.LocalAddr(),
	}
}

func (c *sshConn) LocalAddr() net.Addr                { return c.localAddr }
func (c *sshConn) RemoteAddr() net.Addr               { return c.remoteAddr }
func (c *sshConn) SetDeadline(t time.Time) error      { return nil }
func (c *sshConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *sshConn) SetWriteDeadline(t time.Time) error { return nil }

// DialSSH connects to an SSH server and returns a net.Conn backed by an SSH
// session channel. It tries the SSH agent, then common key files.
func DialSSH(addr, user string) (net.Conn, error) {
	if user == "" {
		user = os.Getenv("USER")
	}

	authMethods := collectAuthMethods()
	if len(authMethods) == 0 {
		return nil, fmt.Errorf("ssh: no auth methods available (no agent, no key files)")
	}

	config := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	conn, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("ssh dial: %w", err)
	}

	session, _, err := conn.OpenChannel("session", nil)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh open channel: %w", err)
	}

	return &sshClientConn{
		Channel:    session,
		client:     conn,
		remoteAddr: conn.RemoteAddr(),
		localAddr:  conn.LocalAddr(),
	}, nil
}

type sshClientConn struct {
	ssh.Channel
	client     *ssh.Client
	remoteAddr net.Addr
	localAddr  net.Addr
}

func (c *sshClientConn) LocalAddr() net.Addr                { return c.localAddr }
func (c *sshClientConn) RemoteAddr() net.Addr               { return c.remoteAddr }
func (c *sshClientConn) SetDeadline(t time.Time) error      { return nil }
func (c *sshClientConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *sshClientConn) SetWriteDeadline(t time.Time) error { return nil }

func (c *sshClientConn) Close() error {
	c.Channel.Close()
	return c.client.Close()
}

func collectAuthMethods() []ssh.AuthMethod {
	var methods []ssh.AuthMethod

	// Try SSH agent
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		agentConn, err := net.Dial("unix", sock)
		if err == nil {
			methods = append(methods, ssh.PublicKeysCallback(
				agent.NewClient(agentConn).Signers,
			))
		}
	}

	// Try common key files
	home, _ := os.UserHomeDir()
	if home != "" {
		for _, name := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
			path := home + "/.ssh/" + name
			key, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			signer, err := ssh.ParsePrivateKey(key)
			if err != nil {
				continue
			}
			methods = append(methods, ssh.PublicKeys(signer))
		}
	}

	return methods
}

func loadOrGenerateHostKey(path string) (ssh.Signer, error) {
	if path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			return ssh.ParsePrivateKey(data)
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
		// File doesn't exist — generate and save
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}

	privBytes, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, err
	}

	pemData := pem.EncodeToMemory(privBytes)

	if path != "" {
		if err := os.WriteFile(path, pemData, 0600); err != nil {
			slog.Warn("ssh: could not save generated host key", "path", path, "error", err)
		} else {
			slog.Info("ssh: generated host key", "path", path)
		}
	}

	return ssh.ParsePrivateKey(pemData)
}

func loadAuthorizedKeys(path string) (map[string]bool, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	keys := make(map[string]bool)
	for len(data) > 0 {
		key, _, _, rest, err := ssh.ParseAuthorizedKey(data)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		keys[string(key.Marshal())] = true
		data = rest
	}
	return keys, nil
}


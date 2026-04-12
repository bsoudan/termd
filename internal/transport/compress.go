package transport

import (
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// compressionMagic is sent by clients that want zstd compression.
// The server reads these 4 bytes first; if they match, both sides
// switch to compressed mode. Otherwise the bytes are treated as the
// start of a plain JSON message.
var compressionMagic = []byte{'Z', 'S', 'T', 'D'}

// compressedConn wraps a net.Conn with zstd stream compression.
// Each Write is flushed so the reader can decode messages immediately.
// The encoder maintains its compression window across flushes, so
// cross-message patterns (repetitive JSON keys) are captured.
type compressedConn struct {
	net.Conn
	r *zstd.Decoder
	w *zstd.Encoder
}

func wrapZstd(conn net.Conn) net.Conn {
	r, _ := zstd.NewReader(conn,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderLowmem(true),
	)
	w, _ := zstd.NewWriter(conn,
		zstd.WithEncoderConcurrency(1),
		zstd.WithEncoderLevel(zstd.SpeedDefault),
	)
	return &compressedConn{Conn: conn, r: r, w: w}
}

// NegotiateCompressionClient sends the compression magic and wraps
// the connection in zstd. Called by clients before any protocol data.
func NegotiateCompressionClient(conn net.Conn) net.Conn {
	conn.Write(compressionMagic)
	return wrapZstd(conn)
}

// prefixConn is a net.Conn that prepends saved bytes to the first Read.
type prefixConn struct {
	net.Conn
	prefix []byte
}

func (c *prefixConn) Read(p []byte) (int, error) {
	if len(c.prefix) > 0 {
		n := copy(p, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

// NegotiateCompressionServer reads the first 4 bytes from the
// connection. If they match the compression magic, the connection
// is wrapped in zstd. Otherwise the bytes are prepended back and
// the connection is returned as-is (plain JSON).
func NegotiateCompressionServer(conn net.Conn) net.Conn {
	buf := make([]byte, len(compressionMagic))
	// Short deadline so a misbehaving client doesn't block the accept loop.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err := io.ReadFull(conn, buf)
	conn.SetReadDeadline(time.Time{}) // clear deadline
	if err != nil {
		// Connection closed or error — return as-is, let caller handle.
		return &prefixConn{Conn: conn, prefix: buf}
	}
	if string(buf) == string(compressionMagic) {
		return wrapZstd(conn)
	}
	// Not compression magic — prepend the bytes back.
	return &prefixConn{Conn: conn, prefix: buf}
}

// WrapCompression wraps a client connection with zstd compression.
// For SSH transport, compression is skipped (SSH has its own compression).
func WrapCompression(conn net.Conn) net.Conn {
	return NegotiateCompressionClient(conn)
}

// NeedsCompression returns true if the transport scheme benefits from
// application-level compression. SSH already compresses at the transport
// layer so we skip it.
func NeedsCompression(endpoint string) bool {
	return !strings.HasPrefix(endpoint, "ssh:") && !strings.HasPrefix(endpoint, "ssh://")
}

// MaybeWrapCompression wraps the connection with compression if the
// endpoint scheme benefits from it.
func MaybeWrapCompression(conn net.Conn, endpoint string) net.Conn {
	if NeedsCompression(endpoint) {
		return NegotiateCompressionClient(conn)
	}
	return conn
}

func (c *compressedConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

func (c *compressedConn) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if err != nil {
		return n, err
	}
	return n, c.w.Flush()
}

func (c *compressedConn) Close() error {
	// Close the underlying conn first. This unblocks any pending
	// reads/writes in the zstd encoder/decoder goroutines.
	err := c.Conn.Close()
	safeClose := func(fn func()) {
		defer func() { recover() }()
		fn()
	}
	safeClose(func() { c.w.Close() })
	safeClose(func() { c.r.Close() })
	return err
}

func (c *compressedConn) String() string {
	return fmt.Sprintf("zstd(%v)", c.Conn)
}

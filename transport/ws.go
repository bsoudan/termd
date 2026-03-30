package transport

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/coder/websocket"
)

// wsListener implements net.Listener by running an HTTP server that upgrades
// connections to WebSocket. Each accepted WebSocket is wrapped as a net.Conn.
type wsListener struct {
	httpServer *http.Server
	tcpLn      net.Listener
	conns      chan net.Conn
	addr       net.Addr
	closeOnce  sync.Once
	done       chan struct{}
}

func listenWS(host string) (net.Listener, error) {
	tcpLn, err := net.Listen("tcp", host)
	if err != nil {
		return nil, fmt.Errorf("ws listen: %w", err)
	}
	return listenWSFromListener(tcpLn)
}

// listenWSFromListener creates a WebSocket listener from an existing TCP listener.
func listenWSFromListener(tcpLn net.Listener) (net.Listener, error) {
	wl := &wsListener{
		tcpLn: tcpLn,
		conns: make(chan net.Conn, 16),
		addr:  tcpLn.Addr(),
		done:  make(chan struct{}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", wl.handleUpgrade)

	wl.httpServer = &http.Server{Handler: mux}
	go wl.httpServer.Serve(tcpLn)

	return wl, nil
}

func (wl *wsListener) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}

	nc := websocket.NetConn(context.Background(), c, websocket.MessageBinary)

	select {
	case wl.conns <- nc:
	case <-wl.done:
		c.CloseNow()
	}
}

func (wl *wsListener) Accept() (net.Conn, error) {
	select {
	case conn := <-wl.conns:
		return conn, nil
	case <-wl.done:
		return nil, net.ErrClosed
	}
}

func (wl *wsListener) Close() error {
	var err error
	wl.closeOnce.Do(func() {
		close(wl.done)
		err = wl.httpServer.Close()
	})
	return err
}

func (wl *wsListener) Addr() net.Addr {
	return wl.addr
}

func dialWS(addr string) (net.Conn, error) {
	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, addr, nil)
	if err != nil {
		return nil, fmt.Errorf("ws dial: %w", err)
	}
	return websocket.NetConn(ctx, c, websocket.MessageBinary), nil
}

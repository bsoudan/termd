package tui

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/grandcat/zeroconf"
)

const mdnsService = "_nxtermd._tcp"

// browseServers starts mDNS browsing for nxtermd servers. Each discovery
// is sent to the bubbletea program as a DiscoveredServerMsg. The goroutine
// exits when ctx is cancelled.
func browseServers(ctx context.Context, p *tea.Program) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		slog.Debug("mDNS browse: resolver failed", "error", err)
		return
	}

	entries := make(chan *zeroconf.ServiceEntry)
	go func() {
		for entry := range entries {
			msg := parseDiscoveredEntry(entry)
			if msg.Endpoint != "" {
				p.Send(msg)
			}
		}
	}()

	if err := resolver.Browse(ctx, mdnsService, "local.", entries); err != nil {
		slog.Debug("mDNS browse failed", "error", err)
	}
	<-ctx.Done()
}

// parseDiscoveredEntry converts a zeroconf entry into a DiscoveredServerMsg.
// It prefers TCP, then WS, then direct SSH from TXT records for the endpoint.
// The s= TXT record (if present) is parsed into the Sessions field.
func parseDiscoveredEntry(entry *zeroconf.ServiceEntry) DiscoveredServerMsg {
	name := entry.Instance

	// Parse TXT records for transport-specific ports and session list.
	ports := make(map[string]int)
	var sessions []string
	for _, txt := range entry.Text {
		parts := strings.SplitN(txt, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, val := parts[0], parts[1]
		switch key {
		case "tcp", "ws", "dssh":
			// Take first port if multiple.
			portStr := strings.SplitN(val, ",", 2)[0]
			if p, err := strconv.Atoi(portStr); err == nil {
				ports[key] = p
			}
		case "s":
			for _, name := range strings.Split(val, ",") {
				if name = strings.TrimSpace(name); name != "" {
					sessions = append(sessions, name)
				}
			}
		}
	}

	// Pick the best IP address.
	var host string
	if len(entry.AddrIPv4) > 0 {
		host = entry.AddrIPv4[0].String()
	} else if len(entry.AddrIPv6) > 0 {
		host = entry.AddrIPv6[0].String()
	} else {
		host = strings.TrimSuffix(entry.HostName, ".")
	}

	// Build endpoint: prefer tcp, then ws, then dssh.
	var endpoint string
	for _, scheme := range []string{"tcp", "ws", "dssh"} {
		if p, ok := ports[scheme]; ok {
			switch scheme {
			case "tcp":
				endpoint = fmt.Sprintf("tcp:%s:%d", host, p)
			case "ws":
				endpoint = fmt.Sprintf("ws://%s:%d", host, p)
			case "dssh":
				endpoint = fmt.Sprintf("dssh://%s:%d", host, p)
			}
			break
		}
	}

	// Fallback to the primary mDNS port with tcp.
	if endpoint == "" && entry.Port > 0 {
		endpoint = fmt.Sprintf("tcp:%s:%d", host, entry.Port)
	}

	return DiscoveredServerMsg{Name: name, Endpoint: endpoint, Sessions: sessions}
}

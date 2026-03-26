package main

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v2"
	"termd/frontend/client"
	termlog "termd/frontend/log"
	"termd/frontend/protocol"
	"termd/transport"
)

func main() {
	app := &cli.App{
		Name:  "termctl",
		Usage: "control the termd server",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "socket",
				Aliases: []string{"s"},
				Value:   "/tmp/termd.sock",
				Usage:   "server socket path",
				EnvVars: []string{"TERMD_SOCKET"},
			},
			&cli.BoolFlag{
				Name:    "debug",
				Aliases: []string{"d"},
				Usage:   "enable debug logging",
				EnvVars: []string{"TERMD_DEBUG"},
			},
		},
		Before: func(c *cli.Context) error {
			level := slog.LevelWarn
			if c.Bool("debug") {
				level = slog.LevelDebug
			}
			slog.SetDefault(slog.New(termlog.NewHandler(os.Stderr, level, nil)))
			return nil
		},
		Commands: []*cli.Command{
			{
				Name:  "status",
				Usage: "show server status",
				Action: cmdStatus,
			},
			{
				Name:  "region",
				Usage: "manage regions",
				Subcommands: []*cli.Command{
					{Name: "list", Usage: "list regions", Action: cmdRegionList},
					{Name: "spawn", Usage: "spawn a new region", ArgsUsage: "<cmd> [args...]", Action: cmdRegionSpawn},
					{
					Name: "view", Usage: "view region screen", ArgsUsage: "<region_id>",
					Flags: []cli.Flag{
						&cli.BoolFlag{Name: "plain", Aliases: []string{"p"}, Usage: "plain text (no colors)"},
					},
					Action: cmdRegionView,
				},
					{Name: "kill", Usage: "kill a region", ArgsUsage: "<region_id>", Action: cmdRegionKill},
					{
						Name: "send", Usage: "send input to a region", ArgsUsage: "<region_id> <input>",
						Flags: []cli.Flag{
							&cli.BoolFlag{Name: "e", Usage: "interpret backslash escapes"},
						},
						Action: cmdRegionSend,
					},
				},
			},
			{
				Name:  "client",
				Usage: "manage clients",
				Subcommands: []*cli.Command{
					{Name: "list", Usage: "list clients", Action: cmdClientList},
					{Name: "kill", Usage: "disconnect a client", ArgsUsage: "<client_id>", Action: cmdClientKill},
				},
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func connect(c *cli.Context) (*client.Client, error) {
	spec := c.String("socket")
	if !strings.Contains(spec, ":") {
		spec = "unix:" + spec
	}
	conn, err := transport.Dial(spec)
	if err != nil {
		return nil, err
	}
	return client.New(conn, "termctl"), nil
}

func recvType[T any](cl *client.Client) (T, error) {
	for msg := range cl.Updates() {
		if v, ok := msg.(T); ok {
			return v, nil
		}
	}
	var zero T
	return zero, fmt.Errorf("connection closed")
}

func cmdStatus(c *cli.Context) error {
	cl, err := connect(c)
	if err != nil {
		return err
	}
	defer cl.Close()

	_ = cl.Send(protocol.StatusRequest{Type: "status_request"})
	resp, err := recvType[protocol.StatusResponse](cl)
	if err != nil {
		return err
	}
	if resp.Error {
		return fmt.Errorf("%s", resp.Message)
	}

	d := time.Duration(resp.UptimeSeconds) * time.Second
	fmt.Printf("PID:      %d\n", resp.Pid)
	fmt.Printf("Uptime:   %s\n", d.String())
	fmt.Printf("Socket:   %s\n", resp.SocketPath)
	fmt.Printf("Clients:  %d\n", resp.NumClients)
	fmt.Printf("Regions:  %d\n", resp.NumRegions)
	return nil
}

func cmdRegionList(c *cli.Context) error {
	cl, err := connect(c)
	if err != nil {
		return err
	}
	defer cl.Close()

	_ = cl.Send(protocol.ListRegionsRequest{Type: "list_regions_request"})
	resp, err := recvType[protocol.ListRegionsResponse](cl)
	if err != nil {
		return err
	}
	if resp.Error {
		return fmt.Errorf("%s", resp.Message)
	}

	if len(resp.Regions) == 0 {
		fmt.Println("no regions")
		return nil
	}

	fmt.Printf("%-36s  %-10s  %-30s  %s\n", "ID", "NAME", "CMD", "PID")
	for _, r := range resp.Regions {
		fmt.Printf("%-36s  %-10s  %-30s  %d\n", r.RegionID, r.Name, r.Cmd, r.Pid)
	}
	return nil
}

func cmdRegionSpawn(c *cli.Context) error {
	if c.NArg() < 1 {
		return fmt.Errorf("usage: termctl region spawn <cmd> [args...]")
	}
	cmd := c.Args().First()
	args := c.Args().Tail()

	cl, err := connect(c)
	if err != nil {
		return err
	}
	defer cl.Close()

	_ = cl.Send(protocol.SpawnRequest{Type: "spawn_request", Cmd: cmd, Args: args})
	resp, err := recvType[protocol.SpawnResponse](cl)
	if err != nil {
		return err
	}
	if resp.Error {
		return fmt.Errorf("spawn failed: %s", resp.Message)
	}

	fmt.Println(resp.RegionID)
	return nil
}

func cmdRegionView(c *cli.Context) error {
	if c.NArg() < 1 {
		return fmt.Errorf("usage: termctl region view <region_id>")
	}
	regionID := c.Args().First()

	cl, err := connect(c)
	if err != nil {
		return err
	}
	defer cl.Close()

	_ = cl.Send(protocol.GetScreenRequest{Type: "get_screen_request", RegionID: regionID})
	resp, err := recvType[protocol.GetScreenResponse](cl)
	if err != nil {
		return err
	}
	if resp.Error {
		return fmt.Errorf("%s", resp.Message)
	}

	if c.Bool("plain") || len(resp.Cells) == 0 {
		for _, line := range resp.Lines {
			fmt.Println(strings.TrimRight(line, " "))
		}
		return nil
	}

	// Render with ANSI color sequences
	for _, row := range resp.Cells {
		fmt.Println(renderColoredLine(row))
	}
	return nil
}

func renderColoredLine(row []protocol.ScreenCell) string {
	var sb strings.Builder
	var curFg, curBg string
	var curA uint8

	for _, cell := range row {
		if cell.Fg != curFg || cell.Bg != curBg || cell.A != curA {
			sb.WriteString(protocol.CellSGR(cell.Fg, cell.Bg, cell.A))
			curFg, curBg, curA = cell.Fg, cell.Bg, cell.A
		}
		ch := cell.Char
		if ch == "" || ch == "\x00" {
			ch = " "
		}
		sb.WriteString(ch)
	}
	if curFg != "" || curBg != "" || curA != 0 {
		sb.WriteString("\x1b[m")
	}

	return strings.TrimRight(sb.String(), " ")
}

func cmdRegionKill(c *cli.Context) error {
	if c.NArg() < 1 {
		return fmt.Errorf("usage: termctl region kill <region_id>")
	}
	regionID := c.Args().First()

	cl, err := connect(c)
	if err != nil {
		return err
	}
	defer cl.Close()

	_ = cl.Send(protocol.KillRegionRequest{Type: "kill_region_request", RegionID: regionID})
	resp, err := recvType[protocol.KillRegionResponse](cl)
	if err != nil {
		return err
	}
	if resp.Error {
		return fmt.Errorf("%s", resp.Message)
	}

	fmt.Println("killed")
	return nil
}

func cmdRegionSend(c *cli.Context) error {
	if c.NArg() < 2 {
		return fmt.Errorf("usage: termctl region send [-e] <region_id> <input>")
	}
	regionID := c.Args().Get(0)
	input := c.Args().Get(1)

	if c.Bool("e") {
		input = interpretEscapes(input)
	}

	cl, err := connect(c)
	if err != nil {
		return err
	}
	defer cl.Close()

	data := base64.StdEncoding.EncodeToString([]byte(input))
	_ = cl.Send(protocol.InputMsg{Type: "input", RegionID: regionID, Data: data})
	return nil
}

func cmdClientList(c *cli.Context) error {
	cl, err := connect(c)
	if err != nil {
		return err
	}
	defer cl.Close()

	_ = cl.Send(protocol.ListClientsRequest{Type: "list_clients_request"})
	resp, err := recvType[protocol.ListClientsResponse](cl)
	if err != nil {
		return err
	}
	if resp.Error {
		return fmt.Errorf("%s", resp.Message)
	}

	fmt.Printf("%-4s  %-15s  %-10s  %-6s  %-18s  %s\n", "ID", "HOSTNAME", "USERNAME", "PID", "PROCESS", "REGION")
	for _, cl := range resp.Clients {
		region := cl.SubscribedRegionID
		if region == "" {
			region = "(none)"
		}
		fmt.Printf("%-4d  %-15s  %-10s  %-6d  %-18s  %s\n",
			cl.ClientID, cl.Hostname, cl.Username, cl.Pid, cl.Process, region)
	}
	return nil
}

func cmdClientKill(c *cli.Context) error {
	if c.NArg() < 1 {
		return fmt.Errorf("usage: termctl client kill <client_id>")
	}
	id, err := strconv.ParseUint(c.Args().First(), 10, 32)
	if err != nil {
		return fmt.Errorf("invalid client_id: %w", err)
	}

	cl, err := connect(c)
	if err != nil {
		return err
	}
	defer cl.Close()

	_ = cl.Send(protocol.KillClientRequest{Type: "kill_client_request", ClientID: uint32(id)})
	resp, err := recvType[protocol.KillClientResponse](cl)
	if err != nil {
		return err
	}
	if resp.Error {
		return fmt.Errorf("%s", resp.Message)
	}

	fmt.Println("killed")
	return nil
}

func interpretEscapes(s string) string {
	var buf strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				buf.WriteByte('\n')
			case 'r':
				buf.WriteByte('\r')
			case 't':
				buf.WriteByte('\t')
			case '\\':
				buf.WriteByte('\\')
			case 'x':
				if i+2 < len(s) {
					if b, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
						buf.WriteByte(byte(b))
						i += 2
					} else {
						buf.WriteByte('\\')
						buf.WriteByte('x')
					}
				}
			case '0':
				// Octal: up to 3 digits
				end := i + 1
				for end < len(s) && end < i+4 && s[end] >= '0' && s[end] <= '7' {
					end++
				}
				if end > i+1 {
					if b, err := strconv.ParseUint(s[i+1:end], 8, 8); err == nil {
						buf.WriteByte(byte(b))
						i = end - 1
					}
				} else {
					buf.WriteByte('\x00')
				}
			default:
				buf.WriteByte('\\')
				buf.WriteByte(s[i])
			}
		} else {
			buf.WriteByte(s[i])
		}
		i++
	}
	return buf.String()
}

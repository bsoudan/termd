package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v3"
	"termd/config"
	"termd/frontend/client"
	termlog "termd/frontend/log"
	"termd/frontend/protocol"
	"termd/transport"
)

var version = "dev"

func main() {
	app := &cli.Command{
		Name:    "termctl",
		Usage:   "control the termd server",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "config",
				Usage: "config file path (default: ~/.config/termd/server.toml)",
			},
			&cli.StringFlag{
				Name:    "socket",
				Aliases: []string{"s"},
				Value:   "/tmp/termd.sock",
				Usage:   "server address (unix path or transport spec)",
				Sources: cli.EnvVars("TERMD_SOCKET"),
			},
			&cli.BoolFlag{
				Name:    "debug",
				Aliases: []string{"d"},
				Usage:   "enable debug logging",
				Sources: cli.EnvVars("TERMD_DEBUG"),
			},
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			// Load config and apply defaults for unset flags
			cfg, err := config.LoadServerConfig(cmd.String("config"))
			if err != nil {
				return ctx, fmt.Errorf("config: %w", err)
			}
			if !cmd.IsSet("socket") && cfg.Termctl.Connect != "" {
				cmd.Set("socket", cfg.Termctl.Connect)
			}

			debug := cmd.Bool("debug") || cfg.Termctl.Debug
			level := slog.LevelWarn
			if debug {
				level = slog.LevelDebug
			}
			slog.SetDefault(slog.New(termlog.NewHandler(os.Stderr, level, nil)))
			transport.InstallStackDump("termctl")
			return ctx, nil
		},
		Commands: []*cli.Command{
			{
				Name:   "status",
				Usage:  "show server status",
				Action: cmdStatus,
			},
			{
				Name:  "session",
				Usage: "manage sessions",
				Commands: []*cli.Command{
					{Name: "list", Usage: "list sessions", Action: cmdSessionList},
				},
			},
			{
				Name:  "region",
				Usage: "manage regions",
				Commands: []*cli.Command{
					{
						Name: "list", Usage: "list regions",
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "session", Aliases: []string{"S"}, Usage: "filter by session name"},
						},
						Action: cmdRegionList,
					},
					{
						Name: "spawn", Usage: "spawn a new region", ArgsUsage: "<cmd> [args...]",
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "session", Aliases: []string{"S"}, Usage: "session to spawn into"},
						},
						Action: cmdRegionSpawn,
					},
					{
						Name: "view", Usage: "view region screen", ArgsUsage: "<region_id>",
						Flags: []cli.Flag{
							&cli.BoolFlag{Name: "plain", Aliases: []string{"p"}, Usage: "plain text (no colors)"},
						},
						Action: cmdRegionView,
					},
					{Name: "kill", Usage: "kill a region", ArgsUsage: "<region_id>", Action: cmdRegionKill},
					{Name: "scrollback", Usage: "view scrollback buffer", ArgsUsage: "<region_id>", Action: cmdRegionScrollback},
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
				Commands: []*cli.Command{
					{Name: "list", Usage: "list clients", Action: cmdClientList},
					{Name: "kill", Usage: "disconnect a client", ArgsUsage: "<client_id>", Action: cmdClientKill},
				},
			},
		},
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func connect(cmd *cli.Command) (*client.Client, error) {
	spec := cmd.String("socket")
	if !strings.Contains(spec, ":") {
		spec = "unix:" + spec
	}
	conn, err := transport.Dial(spec)
	if err != nil {
		return nil, err
	}
	cl := client.New(conn)
	cl.SendIdentify("termctl")
	return cl, nil
}

func recvType[T any](cl *client.Client) (T, error) {
	for msg := range cl.Recv() {
		if v, ok := msg.Payload.(T); ok {
			return v, nil
		}
	}
	var zero T
	return zero, fmt.Errorf("connection closed")
}

func cmdStatus(_ context.Context, cmd *cli.Command) error {
	cl, err := connect(cmd)
	if err != nil {
		return err
	}
	defer cl.Close()

	_ = cl.Send(protocol.StatusRequest{})
	resp, err := recvType[protocol.StatusResponse](cl)
	if err != nil {
		return err
	}
	if resp.Error {
		return fmt.Errorf("%s", resp.Message)
	}

	d := time.Duration(resp.UptimeSeconds) * time.Second
	fmt.Printf("Hostname:  %s\n", resp.Hostname)
	fmt.Printf("Version:   %s\n", resp.Version)
	fmt.Printf("PID:       %d\n", resp.Pid)
	fmt.Printf("Uptime:    %s\n", d.String())
	fmt.Printf("Listeners: %s\n", resp.SocketPath)
	fmt.Printf("Clients:   %d\n", resp.NumClients)
	fmt.Printf("Regions:   %d\n", resp.NumRegions)
	fmt.Printf("Sessions:  %d\n", resp.NumSessions)
	return nil
}

func cmdSessionList(_ context.Context, cmd *cli.Command) error {
	cl, err := connect(cmd)
	if err != nil {
		return err
	}
	defer cl.Close()

	_ = cl.Send(protocol.ListSessionsRequest{})
	resp, err := recvType[protocol.ListSessionsResponse](cl)
	if err != nil {
		return err
	}
	if resp.Error {
		return fmt.Errorf("%s", resp.Message)
	}

	if len(resp.Sessions) == 0 {
		fmt.Println("no sessions")
		return nil
	}

	fmt.Printf("%-20s  %s\n", "NAME", "REGIONS")
	for _, s := range resp.Sessions {
		fmt.Printf("%-20s  %d\n", s.Name, s.NumRegions)
	}
	return nil
}

func cmdRegionList(_ context.Context, cmd *cli.Command) error {
	cl, err := connect(cmd)
	if err != nil {
		return err
	}
	defer cl.Close()

	_ = cl.Send(protocol.ListRegionsRequest{Session: cmd.String("session")})
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

	fmt.Printf("%-36s  %-10s  %-10s  %-30s  %s\n", "ID", "SESSION", "NAME", "CMD", "PID")
	for _, r := range resp.Regions {
		fmt.Printf("%-36s  %-10s  %-10s  %-30s  %d\n", r.RegionID, r.Session, r.Name, r.Cmd, r.Pid)
	}
	return nil
}

func cmdRegionSpawn(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() < 1 {
		return fmt.Errorf("usage: termctl region spawn <cmd> [args...]")
	}
	spawnCmd := cmd.Args().First()
	args := cmd.Args().Tail()

	cl, err := connect(cmd)
	if err != nil {
		return err
	}
	defer cl.Close()

	_ = cl.Send(protocol.SpawnRequest{Session: cmd.String("session"), Cmd: spawnCmd, Args: args})
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

func cmdRegionView(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() < 1 {
		return fmt.Errorf("usage: termctl region view <region_id>")
	}
	regionID := cmd.Args().First()

	cl, err := connect(cmd)
	if err != nil {
		return err
	}
	defer cl.Close()

	_ = cl.Send(protocol.GetScreenRequest{RegionID: regionID})
	resp, err := recvType[protocol.GetScreenResponse](cl)
	if err != nil {
		return err
	}
	if resp.Error {
		return fmt.Errorf("%s", resp.Message)
	}

	if cmd.Bool("plain") || len(resp.Cells) == 0 {
		for _, line := range resp.Lines {
			fmt.Println(strings.TrimRight(line, " "))
		}
		return nil
	}

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

func cmdRegionKill(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() < 1 {
		return fmt.Errorf("usage: termctl region kill <region_id>")
	}
	regionID := cmd.Args().First()

	cl, err := connect(cmd)
	if err != nil {
		return err
	}
	defer cl.Close()

	_ = cl.Send(protocol.KillRegionRequest{RegionID: regionID})
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

func cmdRegionScrollback(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() < 1 {
		return fmt.Errorf("usage: termctl region scrollback <region_id>")
	}
	regionID := cmd.Args().First()

	cl, err := connect(cmd)
	if err != nil {
		return err
	}
	defer cl.Close()

	_ = cl.Send(protocol.GetScrollbackRequest{RegionID: regionID})
	resp, err := recvType[protocol.GetScrollbackResponse](cl)
	if err != nil {
		return err
	}
	if resp.Error {
		return fmt.Errorf("%s", resp.Message)
	}

	for _, row := range resp.Lines {
		var line strings.Builder
		for _, cell := range row {
			ch := cell.Char
			if ch == "" || ch == "\x00" {
				ch = " "
			}
			line.WriteString(ch)
		}
		fmt.Println(strings.TrimRight(line.String(), " "))
	}
	return nil
}

func cmdRegionSend(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() < 2 {
		return fmt.Errorf("usage: termctl region send [-e] <region_id> <input>")
	}
	regionID := cmd.Args().Get(0)
	input := cmd.Args().Get(1)

	if cmd.Bool("e") {
		input = interpretEscapes(input)
	}

	cl, err := connect(cmd)
	if err != nil {
		return err
	}
	defer cl.Close()

	data := base64.StdEncoding.EncodeToString([]byte(input))
	_ = cl.Send(protocol.InputMsg{RegionID: regionID, Data: data})
	return nil
}

func cmdClientList(_ context.Context, cmd *cli.Command) error {
	cl, err := connect(cmd)
	if err != nil {
		return err
	}
	defer cl.Close()

	_ = cl.Send(protocol.ListClientsRequest{})
	resp, err := recvType[protocol.ListClientsResponse](cl)
	if err != nil {
		return err
	}
	if resp.Error {
		return fmt.Errorf("%s", resp.Message)
	}

	fmt.Printf("%-4s  %-15s  %-10s  %-6s  %-18s  %-10s  %s\n", "ID", "HOSTNAME", "USERNAME", "PID", "PROCESS", "SESSION", "REGION")
	for _, cl := range resp.Clients {
		region := cl.SubscribedRegionID
		if region == "" {
			region = "(none)"
		}
		session := cl.Session
		if session == "" {
			session = "(none)"
		}
		fmt.Printf("%-4d  %-15s  %-10s  %-6d  %-18s  %-10s  %s\n",
			cl.ClientID, cl.Hostname, cl.Username, cl.Pid, cl.Process, session, region)
	}
	return nil
}

func cmdClientKill(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() < 1 {
		return fmt.Errorf("usage: termctl client kill <client_id>")
	}
	id, err := strconv.ParseUint(cmd.Args().First(), 10, 32)
	if err != nil {
		return fmt.Errorf("invalid client_id: %w", err)
	}

	cl, err := connect(cmd)
	if err != nil {
		return err
	}
	defer cl.Close()

	_ = cl.Send(protocol.KillClientRequest{ClientID: uint32(id)})
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

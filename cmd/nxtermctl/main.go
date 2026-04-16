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
	"nxtermd/internal/config"
	"nxtermd/internal/client"
	"nxtermd/internal/nxlog"
	"nxtermd/internal/protocol"
	"nxtermd/internal/transport"
)

var version = "dev"

func main() {
	app := &cli.Command{
		Name:    "nxtermctl",
		Usage:   "control the nxtermd server",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "config",
				Usage: "config file path (default: ~/.config/nxtermd/server.toml)",
			},
			&cli.StringFlag{
				Name:    "socket",
				Aliases: []string{"s"},
				Value:   config.DefaultSocket(),
				Usage:   "server address (unix path or transport spec)",
				Sources: cli.EnvVars("NXTERMD_SOCKET"),
			},
			&cli.BoolFlag{
				Name:    "debug",
				Aliases: []string{"d"},
				Usage:   "enable debug logging",
				Sources: cli.EnvVars("NXTERMD_DEBUG"),
			},
			&cli.BoolFlag{
				Name:  "show-config",
				Usage: "print the effective configuration with sources and exit",
			},
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			if cmd.Bool("show-config") {
				if err := showTermctlConfig(cmd); err != nil {
					return ctx, err
				}
				os.Exit(0)
			}
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
			slog.SetDefault(slog.New(nxlog.NewHandler(os.Stderr, level, nil)))
			transport.InstallStackDump("nxtermctl")
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
						Name: "spawn", Usage: "spawn a new region", ArgsUsage: "[program-name]",
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
				Name:  "program",
				Usage: "manage programs",
				Commands: []*cli.Command{
					{Name: "list", Usage: "list configured programs", Action: cmdProgramList},
					{
						Name: "add", Usage: "add a program", ArgsUsage: "<name> <cmd> [args...]",
						Flags: []cli.Flag{
							&cli.StringSliceFlag{Name: "env", Aliases: []string{"e"}, Usage: "environment variable (KEY=VAL)"},
						},
						Action: cmdProgramAdd,
					},
					{Name: "remove", Usage: "remove a program", ArgsUsage: "<name>", Action: cmdProgramRemove},
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
			{
				Name:      "proxy",
				Usage:     "io.Copy stdin/stdout to a local nxtermd socket (used as the remote command for ssh:// transport)",
				ArgsUsage: "[SOCKET] [NONCE]",
				Description: `Bridges stdin/stdout to a local nxtermd unix socket. Intended to be
invoked as the remote command of an ssh connection by nxterm's ssh://
transport. SOCKET is optional and defaults to the first unix listen
address in server.toml or /tmp/nxtermd.sock. NONCE is echoed back in the
ready sentinel so the calling client can detect the boundary between
ssh authentication chatter and the start of the data stream.`,
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:  "base64",
						Usage: "base64-encode protocol lines (used by Windows ConPTY transport)",
					},
				},
				Action: cmdProxy,
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
	cl := client.New(transport.WrapCompression(conn))
	cl.SendIdentify("nxtermctl")
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

	snap, err := recvType[protocol.TreeSnapshot](cl)
	if err != nil {
		return err
	}

	srv := snap.Tree.Server
	uptime := time.Since(time.Unix(srv.StartTime, 0)).Truncate(time.Second)
	fmt.Printf("Hostname:  %s\n", srv.Hostname)
	fmt.Printf("Version:   %s\n", srv.Version)
	fmt.Printf("PID:       %d\n", srv.Pid)
	fmt.Printf("Uptime:    %s\n", uptime.String())
	fmt.Printf("Listeners: %s\n", srv.SocketPath)
	fmt.Printf("Clients:   %d\n", len(snap.Tree.Clients))
	fmt.Printf("Regions:   %d\n", len(snap.Tree.Regions))
	fmt.Printf("Sessions:  %d\n", len(snap.Tree.Sessions))
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
	programName := cmd.Args().First()
	if programName == "" {
		programName = "default"
	}

	cl, err := connect(cmd)
	if err != nil {
		return err
	}
	defer cl.Close()

	_ = cl.Send(protocol.SpawnRequest{Session: cmd.String("session"), Program: programName})
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
		return fmt.Errorf("usage: nxtermctl region view <region_id>")
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
		return fmt.Errorf("usage: nxtermctl region kill <region_id>")
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
		return fmt.Errorf("usage: nxtermctl region scrollback <region_id>")
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
		return fmt.Errorf("usage: nxtermctl region send [-e] <region_id> <input>")
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
		return fmt.Errorf("usage: nxtermctl client kill <client_id>")
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

func cmdProgramList(_ context.Context, cmd *cli.Command) error {
	cl, err := connect(cmd)
	if err != nil {
		return err
	}
	defer cl.Close()

	_ = cl.Send(protocol.ListProgramsRequest{})
	resp, err := recvType[protocol.ListProgramsResponse](cl)
	if err != nil {
		return err
	}
	if resp.Error {
		return fmt.Errorf("%s", resp.Message)
	}

	if len(resp.Programs) == 0 {
		fmt.Println("no programs")
		return nil
	}

	fmt.Printf("%-20s  %s\n", "NAME", "CMD")
	for _, p := range resp.Programs {
		fmt.Printf("%-20s  %s\n", p.Name, p.Cmd)
	}
	return nil
}

func cmdProgramAdd(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() < 2 {
		return fmt.Errorf("usage: nxtermctl program add <name> <cmd> [args...]")
	}
	name := cmd.Args().Get(0)
	progCmd := cmd.Args().Get(1)
	args := cmd.Args().Slice()[2:]

	var env map[string]string
	if envSlice := cmd.StringSlice("env"); len(envSlice) > 0 {
		env = make(map[string]string)
		for _, e := range envSlice {
			k, v, ok := strings.Cut(e, "=")
			if !ok {
				return fmt.Errorf("invalid env format %q (expected KEY=VAL)", e)
			}
			env[k] = v
		}
	}

	cl, err := connect(cmd)
	if err != nil {
		return err
	}
	defer cl.Close()

	_ = cl.Send(protocol.AddProgramRequest{Name: name, Cmd: progCmd, Args: args, Env: env})
	resp, err := recvType[protocol.AddProgramResponse](cl)
	if err != nil {
		return err
	}
	if resp.Error {
		return fmt.Errorf("%s", resp.Message)
	}
	fmt.Printf("added %s\n", name)
	return nil
}

func cmdProgramRemove(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() < 1 {
		return fmt.Errorf("usage: nxtermctl program remove <name>")
	}
	name := cmd.Args().First()

	cl, err := connect(cmd)
	if err != nil {
		return err
	}
	defer cl.Close()

	_ = cl.Send(protocol.RemoveProgramRequest{Name: name})
	resp, err := recvType[protocol.RemoveProgramResponse](cl)
	if err != nil {
		return err
	}
	if resp.Error {
		return fmt.Errorf("%s", resp.Message)
	}
	fmt.Printf("removed %s\n", name)
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

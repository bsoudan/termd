const std = @import("std");
const vt = @import("ghostty-vt");
const protocol = @import("protocol.zig");

const c = @cImport({
    @cDefine("_GNU_SOURCE", "1");
    @cInclude("stdlib.h");
    @cInclude("unistd.h");
    @cInclude("sys/ioctl.h");
    @cInclude("termios.h");
    @cInclude("signal.h");
    @cInclude("sys/wait.h");
});

const log = std.log.scoped(.region);

pub const Region = struct {
    alloc: std.mem.Allocator,
    id: [36]u8,
    name: []const u8,
    cmd: []const u8,
    width: u16,
    height: u16,
    pty_master: std.posix.fd_t,
    child_pid: std.posix.pid_t,
    terminal: vt.Terminal,
    vt_stream: vt.ReadonlyStream,
    mutex: std.Thread.Mutex,
    output_notify_read: std.posix.fd_t,
    output_notify_write: std.posix.fd_t,
    alive: std.atomic.Value(bool),
    reader_thread: std.Thread,

    pub fn init(
        alloc: std.mem.Allocator,
        cmd: []const u8,
        args: []const []const u8,
        width: u16,
        height: u16,
    ) !*Region {
        // Create notify pipe with CLOEXEC so child processes don't inherit it
        const notify_fds = try std.posix.pipe2(.{ .CLOEXEC = true });
        errdefer {
            std.posix.close(notify_fds[0]);
            std.posix.close(notify_fds[1]);
        }

        // Open PTY
        const pty = try openPty(width, height);
        errdefer std.posix.close(pty.master);

        // Prepare null-terminated strings for exec (before fork, COW-safe)
        const cmd_z = try alloc.dupeZ(u8, cmd);
        defer alloc.free(cmd_z);
        const argv_buf = try alloc.alloc(?[*:0]const u8, args.len + 2);
        defer alloc.free(argv_buf);
        argv_buf[0] = cmd_z.ptr;
        var arg_zs = try alloc.alloc([:0]u8, args.len);
        defer {
            for (arg_zs) |z| alloc.free(z);
            alloc.free(arg_zs);
        }
        for (args, 0..) |arg, i| {
            arg_zs[i] = try alloc.dupeZ(u8, arg);
            argv_buf[i + 1] = arg_zs[i].ptr;
        }
        argv_buf[args.len + 1] = null;

        // Fork child
        const pid = c.fork();
        if (pid < 0) return error.ForkFailed;
        if (pid == 0) {
            // ── Child process ──
            std.posix.close(pty.master);
            std.posix.close(notify_fds[0]);
            std.posix.close(notify_fds[1]);
            _ = c.setsid();
            _ = c.ioctl(pty.slave, c.TIOCSCTTY, @as(c_int, 0));
            _ = c.dup2(pty.slave, 0);
            _ = c.dup2(pty.slave, 1);
            _ = c.dup2(pty.slave, 2);
            if (pty.slave > 2) _ = c.close(pty.slave);
            _ = c.setenv("TERM", "xterm-256color", 1);
            _ = c.execvp(cmd_z.ptr, @ptrCast(argv_buf.ptr));
            c._exit(1);
            unreachable;
        }

        // ── Parent process ──
        std.posix.close(pty.slave);
        log.debug("spawned child pid={d} cmd={s}", .{ pid, cmd });

        // Allocate region on heap (stable address for terminal pointer)
        const region = try alloc.create(Region);
        errdefer alloc.destroy(region);
        region.* = .{
            .alloc = alloc,
            .id = generateId(),
            .name = try alloc.dupe(u8, extractName(cmd)),
            .cmd = try alloc.dupe(u8, cmd),
            .width = width,
            .height = height,
            .pty_master = pty.master,
            .child_pid = pid,
            .terminal = try vt.Terminal.init(alloc, .{
                .cols = width,
                .rows = height,
            }),
            .vt_stream = undefined, // initialized below
            .mutex = .{},
            .output_notify_read = notify_fds[0],
            .output_notify_write = notify_fds[1],
            .alive = std.atomic.Value(bool).init(true),
            .reader_thread = undefined,
        };
        region.vt_stream = region.terminal.vtStream();

        // Start PTY reader thread (joined in deinit)
        region.reader_thread = try std.Thread.spawn(.{}, ptyReaderThread, .{region});

        return region;
    }

    pub fn deinit(self: *Region) void {
        // Signal reader thread to stop, then close the PTY master to unblock
        // the read() call. Join the thread to ensure it's done before freeing.
        self.alive.store(false, .release);
        std.posix.close(self.pty_master);
        self.reader_thread.join();

        // Reader thread has exited and already called waitpid on the child.
        std.posix.close(self.output_notify_read);
        std.posix.close(self.output_notify_write);
        self.terminal.deinit(self.alloc);
        self.alloc.free(self.name);
        self.alloc.free(self.cmd);
        self.alloc.destroy(self);
    }

    /// Write raw bytes to the PTY (keyboard input from frontend).
    pub fn writeInput(self: *Region, data: []const u8) !void {
        var offset: usize = 0;
        while (offset < data.len) {
            offset += std.posix.write(self.pty_master, data[offset..]) catch |err| {
                log.debug("pty write error: {}", .{err});
                return err;
            };
        }
    }

    /// Resize the PTY and terminal.
    pub fn resize(self: *Region, alloc: std.mem.Allocator, width: u16, height: u16) !void {
        self.mutex.lock();
        defer self.mutex.unlock();

        var ws: c.struct_winsize = std.mem.zeroes(c.struct_winsize);
        ws.ws_col = width;
        ws.ws_row = height;
        _ = c.ioctl(self.pty_master, c.TIOCSWINSZ, &ws);
        _ = c.kill(-self.child_pid, c.SIGWINCH);

        try self.terminal.resize(alloc, width, height);
        self.width = width;
        self.height = height;
        log.debug("region {s} resized to {d}x{d}", .{ &self.id, width, height });
    }

    pub const Snapshot = struct {
        lines: [][]const u8,
        cursor_row: u16,
        cursor_col: u16,
    };

    /// Snapshot the terminal screen as plain-text lines plus cursor position.
    /// Caller owns the returned line slices.
    pub fn snapshot(self: *Region, alloc: std.mem.Allocator) !Snapshot {
        self.mutex.lock();
        defer self.mutex.unlock();

        const screen = self.terminal.screens.active;
        const cursor_row: u16 = @intCast(screen.cursor.y);
        const cursor_col: u16 = @intCast(screen.cursor.x);

        var lines = try alloc.alloc([]const u8, self.height);
        var lines_written: usize = 0;
        errdefer {
            for (lines[0..lines_written]) |l| alloc.free(l);
            alloc.free(lines);
        }

        for (0..self.height) |y| {
            var buf: std.ArrayList(u8) = .{};
            errdefer buf.deinit(alloc);

            for (0..self.width) |x| {
                const pin = screen.pages.pin(.{ .active = .{
                    .x = @intCast(x),
                    .y = @intCast(y),
                } }) orelse {
                    try buf.append(alloc, ' ');
                    continue;
                };
                const rc = pin.rowAndCell();
                const cp = rc.cell.codepoint();
                if (cp > 0) {
                    var enc: [4]u8 = undefined;
                    const len = std.unicode.utf8Encode(@intCast(cp), &enc) catch {
                        try buf.append(alloc, '?');
                        continue;
                    };
                    try buf.appendSlice(alloc, enc[0..len]);
                } else {
                    try buf.append(alloc, ' ');
                }
            }

            lines[y] = try buf.toOwnedSlice(alloc);
            lines_written += 1;
        }

        return .{ .lines = lines, .cursor_row = cursor_row, .cursor_col = cursor_col };
    }

    // ── Internal ─────────────────────────────────────────────────────────

    fn ptyReaderThread(region: *Region) void {
        var buf: [4096]u8 = undefined;
        while (region.alive.load(.acquire)) {
            const n = std.posix.read(region.pty_master, &buf) catch {
                break;
            };
            if (n == 0) break; // EOF

            region.mutex.lock();
            region.vt_stream.nextSlice(buf[0..n]) catch |err| {
                log.debug("vt stream error: {}", .{err});
            };
            region.mutex.unlock();

            // Notify server: new data
            _ = std.posix.write(region.output_notify_write, &[_]u8{1}) catch {};
        }

        // Reap child
        _ = c.waitpid(region.child_pid, null, 0);
        // Notify server: region dead
        _ = std.posix.write(region.output_notify_write, &[_]u8{0}) catch {};
        log.debug("region {s} reader thread exiting", .{&region.id});
    }
};

fn openPty(width: u16, height: u16) !struct { master: std.posix.fd_t, slave: std.posix.fd_t } {
    const master = try std.posix.open("/dev/ptmx", .{ .ACCMODE = .RDWR, .NOCTTY = true, .CLOEXEC = true }, 0);
    errdefer std.posix.close(master);

    if (c.grantpt(master) != 0) return error.PtyGrant;
    if (c.unlockpt(master) != 0) return error.PtyUnlock;

    const slave_name = c.ptsname(master) orelse return error.PtyName;
    const slave = try std.posix.open(std.mem.span(slave_name), .{ .ACCMODE = .RDWR, .NOCTTY = true }, 0);
    errdefer std.posix.close(slave);

    var ws: c.struct_winsize = std.mem.zeroes(c.struct_winsize);
    ws.ws_col = width;
    ws.ws_row = height;
    _ = c.ioctl(master, c.TIOCSWINSZ, &ws);

    return .{ .master = master, .slave = slave };
}

/// Extract the short program name from a full path.
fn extractName(cmd: []const u8) []const u8 {
    if (std.mem.lastIndexOfScalar(u8, cmd, '/')) |i| {
        return cmd[i + 1 ..];
    }
    return cmd;
}

/// Generate a UUID v4 region ID.
pub fn generateId() [36]u8 {
    var bytes: [16]u8 = undefined;
    std.crypto.random.bytes(&bytes);
    bytes[6] = (bytes[6] & 0x0f) | 0x40;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;

    var id: [36]u8 = undefined;
    const hex = "0123456789abcdef";
    var pos: usize = 0;
    for (bytes, 0..) |b, i| {
        if (i == 4 or i == 6 or i == 8 or i == 10) {
            id[pos] = '-';
            pos += 1;
        }
        id[pos] = hex[b >> 4];
        id[pos + 1] = hex[b & 0x0f];
        pos += 2;
    }
    return id;
}

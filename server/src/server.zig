const std = @import("std");
const protocol = @import("protocol.zig");
const Region = @import("region.zig").Region;
const Client = @import("client.zig").Client;

const c = @cImport({
    @cDefine("_GNU_SOURCE", "1");
    @cInclude("unistd.h");
    @cInclude("signal.h");
});

const log = std.log.scoped(.server);

pub const Server = struct {
    alloc: std.mem.Allocator,
    socket_fd: std.posix.fd_t,
    socket_path: []const u8,
    start_time: i64,
    next_client_id: u32, // wraps at u32 max; unlikely to be a problem in practice
    regions: std.StringArrayHashMap(*Region),
    clients: std.ArrayList(*Client),
    running: bool,
    signal_pipe_read: std.posix.fd_t,
    signal_pipe_write: std.posix.fd_t,

    // Global signal pipe write fd, set during init and used by the signal handler.
    var signal_pipe_write_global: std.posix.fd_t = -1;

    fn signalHandler(_: c_int) callconv(.c) void {
        if (signal_pipe_write_global >= 0) {
            _ = std.posix.write(signal_pipe_write_global, &[_]u8{1}) catch {};
        }
    }

    pub fn init(alloc: std.mem.Allocator, socket_path: [:0]const u8) !Server {
        std.posix.unlink(socket_path) catch {};

        const addr = try std.net.Address.initUnix(socket_path);
        const sock = try std.posix.socket(
            std.posix.AF.UNIX,
            std.posix.SOCK.STREAM | std.posix.SOCK.CLOEXEC,
            0,
        );
        errdefer std.posix.close(sock);

        try std.posix.bind(sock, &addr.any, addr.getOsSockLen());
        try std.posix.listen(sock, 5);

        // Create signal self-pipe for graceful shutdown
        const sig_fds = try std.posix.pipe2(.{ .CLOEXEC = true });
        errdefer {
            std.posix.close(sig_fds[0]);
            std.posix.close(sig_fds[1]);
        }
        signal_pipe_write_global = sig_fds[1];

        // Install signal handlers for SIGTERM and SIGINT
        const sa: std.posix.Sigaction = .{
            .handler = .{ .handler = signalHandler },
            .mask = std.posix.sigemptyset(),
            .flags = std.os.linux.SA.RESTART,
        };
        std.posix.sigaction(std.posix.SIG.TERM, &sa, null);
        std.posix.sigaction(std.posix.SIG.INT, &sa, null);

        log.info("listening on {s}", .{socket_path});
        return .{
            .alloc = alloc,
            .socket_fd = sock,
            .socket_path = socket_path,
            .start_time = std.time.timestamp(),
            .next_client_id = 1,
            .regions = .{ .unmanaged = .{}, .allocator = alloc, .ctx = .{} },
            .clients = .{},
            .running = true,
            .signal_pipe_read = sig_fds[0],
            .signal_pipe_write = sig_fds[1],
        };
    }

    pub fn deinit(self: *Server) void {
        for (self.clients.items) |client| client.deinit();
        self.clients.deinit(self.alloc);

        var it = self.regions.iterator();
        while (it.next()) |entry| {
            self.alloc.free(entry.key_ptr.*);
            entry.value_ptr.*.deinit();
        }
        self.regions.deinit();

        std.posix.close(self.socket_fd);
        std.posix.close(self.signal_pipe_read);
        std.posix.close(self.signal_pipe_write);
        signal_pipe_write_global = -1;
    }

    pub fn run(self: *Server) !void {
        while (self.running) {
            // Build poll fd list: [signal_pipe, socket, ...clients, ...region notifies]
            var pollfds: std.ArrayList(std.posix.pollfd) = .{};
            defer pollfds.deinit(self.alloc);

            try pollfds.append(self.alloc, .{ .fd = self.signal_pipe_read, .events = std.posix.POLL.IN, .revents = 0 });
            try pollfds.append(self.alloc, .{ .fd = self.socket_fd, .events = std.posix.POLL.IN, .revents = 0 });

            for (self.clients.items) |client| {
                try pollfds.append(self.alloc, .{ .fd = client.conn_fd, .events = std.posix.POLL.IN, .revents = 0 });
            }

            const client_count = self.clients.items.len;
            var region_keys: std.ArrayList([]const u8) = .{};
            defer region_keys.deinit(self.alloc);
            {
                var rit = self.regions.iterator();
                while (rit.next()) |entry| {
                    try pollfds.append(self.alloc, .{
                        .fd = entry.value_ptr.*.output_notify_read,
                        .events = std.posix.POLL.IN,
                        .revents = 0,
                    });
                    try region_keys.append(self.alloc, entry.key_ptr.*);
                }
            }

            _ = std.posix.poll(pollfds.items, -1) catch |err| {
                log.debug("poll error: {}", .{err});
                continue;
            };

            // 0. Check signal pipe for shutdown
            if (pollfds.items[0].revents & std.posix.POLL.IN != 0) {
                var sig_buf: [64]u8 = undefined;
                _ = std.posix.read(self.signal_pipe_read, &sig_buf) catch {};
                log.info("received shutdown signal", .{});
                self.running = false;
                break;
            }

            // 1. Accept new connections
            if (pollfds.items[1].revents & std.posix.POLL.IN != 0) {
                self.acceptClient() catch |err| {
                    log.debug("accept error: {}", .{err});
                };
            }

            // 2. Read from clients polled this cycle (iterate in reverse so swapRemove is safe)
            {
                var i: usize = client_count;
                while (i > 0) {
                    i -= 1;
                    const cl = self.clients.items[i];
                    if (cl.dead) {
                        cl.deinit();
                        _ = self.clients.swapRemove(i);
                        continue;
                    }
                    const pfd = pollfds.items[2 + i];
                    if (pfd.revents & (std.posix.POLL.IN | std.posix.POLL.HUP | std.posix.POLL.ERR) != 0) {
                        const ok = cl.readAvailable() catch false;
                        if (!ok) {
                            cl.deinit();
                            _ = self.clients.swapRemove(i);
                        }
                    }
                }
            }

            // 3. Handle region notifications
            var dead_keys: std.ArrayList([]const u8) = .{};
            defer dead_keys.deinit(self.alloc);

            for (region_keys.items, 0..) |key, ri| {
                const pfd = pollfds.items[2 + client_count + ri];
                if (pfd.revents & std.posix.POLL.IN == 0) continue;

                const region = self.regions.get(key) orelse continue;

                // Read notify pipe (one batch per poll cycle; poll will re-fire if more)
                var notify_buf: [64]u8 = undefined;
                var is_dead = false;
                const n = std.posix.read(region.output_notify_read, &notify_buf) catch 0;
                for (notify_buf[0..n]) |b| {
                    if (b == 0) is_dead = true;
                }

                // Always send the latest screen state (covers data that
                // arrived in the same batch as the death sentinel).
                self.sendScreenUpdate(region);

                if (is_dead) {
                    dead_keys.append(self.alloc, key) catch {};
                }
            }

            // 4. Destroy dead regions
            for (dead_keys.items) |key| {
                self.destroyRegion(key);
            }
        }
    }

    pub fn spawnRegion(self: *Server, cmd: []const u8, args: []const []const u8) !*Region {
        const region = try Region.init(self.alloc, cmd, args, 80, 24);
        errdefer region.deinit();

        const key = try self.alloc.dupe(u8, &region.id);
        errdefer self.alloc.free(key);
        try self.regions.put(key, region);

        log.info("spawned region {s} cmd={s}", .{ &region.id, cmd });
        return region;
    }

    pub fn destroyRegion(self: *Server, region_id: []const u8) void {
        const entry = self.regions.fetchSwapRemove(region_id) orelse return;

        for (self.clients.items) |client| {
            if (client.subscribed_region_id) |sub_id| {
                if (std.mem.eql(u8, &sub_id, region_id)) {
                    client.subscribed_region_id = null;
                    client.sendMessage(.{ .region_destroyed = .{
                        .region_id = region_id,
                    } });
                }
            }
        }

        log.info("destroyed region {s}", .{region_id});
        self.alloc.free(entry.key);
        entry.value.deinit();
    }

    pub fn findRegion(self: *Server, region_id: []const u8) ?*Region {
        return self.regions.get(region_id);
    }

    pub fn broadcast(self: *Server, msg: protocol.OutboundMessage) void {
        for (self.clients.items) |client| {
            client.sendMessage(msg);
        }
    }

    fn acceptClient(self: *Server) !void {
        const client_fd = try std.posix.accept(self.socket_fd, null, null, std.posix.SOCK.CLOEXEC);
        const id = self.next_client_id;
        self.next_client_id += 1;
        const client = Client.init(self.alloc, client_fd, self, id);
        try self.clients.append(self.alloc, client);
    }

    pub fn killRegion(self: *Server, region_id: []const u8) bool {
        const region = self.regions.get(region_id) orelse return false;
        _ = c.kill(region.child_pid, c.SIGKILL);
        return true;
    }

    pub fn killClient(self: *Server, client_id: u32) bool {
        for (self.clients.items) |cl| {
            if (cl.id == client_id) {
                cl.dead = true;
                return true;
            }
        }
        return false;
    }

    fn sendScreenUpdate(self: *Server, region: *Region) void {
        const snap = region.snapshot(self.alloc) catch return;
        defer {
            for (snap.lines) |l| self.alloc.free(l);
            self.alloc.free(snap.lines);
        }

        for (self.clients.items) |client| {
            if (client.subscribed_region_id) |sub_id| {
                if (std.mem.eql(u8, &sub_id, &region.id)) {
                    client.sendMessage(.{ .screen_update = .{
                        .region_id = &region.id,
                        .cursor_row = snap.cursor_row,
                        .cursor_col = snap.cursor_col,
                        .lines = snap.lines,
                    } });
                }
            }
        }
    }
};

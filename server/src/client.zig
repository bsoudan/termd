const std = @import("std");
const protocol = @import("protocol.zig");
const Server = @import("server.zig").Server;
const Region = @import("region.zig").Region;

const log = std.log.scoped(.client);

pub const Client = struct {
    alloc: std.mem.Allocator,
    conn_fd: std.posix.fd_t,
    server: *Server,
    id: u32,
    hostname: []const u8,
    hostname_owned: bool,
    username: []const u8,
    username_owned: bool,
    client_pid: i32,
    process: []const u8,
    process_owned: bool,
    subscribed_region_id: ?[36]u8,
    read_buf: std.ArrayList(u8),
    dead: bool,

    pub fn init(alloc: std.mem.Allocator, conn_fd: std.posix.fd_t, server: *Server, id: u32) *Client {
        const self = alloc.create(Client) catch @panic("OOM");
        self.* = .{
            .alloc = alloc,
            .conn_fd = conn_fd,
            .server = server,
            .id = id,
            .hostname = "unknown",
            .hostname_owned = false,
            .username = "unknown",
            .username_owned = false,
            .client_pid = 0,
            .process = "unknown",
            .process_owned = false,
            .subscribed_region_id = null,
            .read_buf = .{},
            .dead = false,
        };
        log.debug("client connected fd={d} id={d}", .{ conn_fd, id });
        return self;
    }

    pub fn deinit(self: *Client) void {
        log.debug("client disconnected fd={d} id={d}", .{ self.conn_fd, self.id });
        std.posix.close(self.conn_fd);
        self.read_buf.deinit(self.alloc);
        if (self.hostname_owned) self.alloc.free(self.hostname);
        if (self.username_owned) self.alloc.free(self.username);
        if (self.process_owned) self.alloc.free(self.process);
        self.alloc.destroy(self);
    }

    pub fn readAvailable(self: *Client) !bool {
        var buf: [4096]u8 = undefined;
        const n = std.posix.read(self.conn_fd, &buf) catch |err| {
            log.debug("client read error: {}", .{err});
            return false;
        };
        if (n == 0) return false;

        try self.read_buf.appendSlice(self.alloc, buf[0..n]);

        while (std.mem.indexOfScalar(u8, self.read_buf.items, '\n')) |nl| {
            const line = self.read_buf.items[0..nl];
            self.handleMessage(line) catch |err| {
                log.debug("message handling error: {}", .{err});
            };
            const rest = self.read_buf.items[nl + 1 ..];
            std.mem.copyForwards(u8, self.read_buf.items[0..rest.len], rest);
            self.read_buf.shrinkRetainingCapacity(rest.len);
        }

        return true;
    }

    pub fn sendMessage(self: *Client, msg: protocol.OutboundMessage) void {
        var aw: std.io.Writer.Allocating = .init(self.alloc);
        defer aw.deinit();
        protocol.writeOutbound(&aw.writer, msg) catch |err| {
            log.debug("client send error fd={d}: {}", .{ self.conn_fd, err });
            return;
        };
        aw.writer.flush() catch |err| {
            log.debug("client flush error fd={d}: {}", .{ self.conn_fd, err });
            return;
        };
        const data = aw.writer.buffer[0..aw.writer.end];
        var offset: usize = 0;
        while (offset < data.len) {
            offset += std.posix.write(self.conn_fd, data[offset..]) catch |err| {
                log.debug("client write error fd={d}: {}", .{ self.conn_fd, err });
                return;
            };
        }
    }

    fn handleMessage(self: *Client, line: []const u8) !void {
        var arena = std.heap.ArenaAllocator.init(self.alloc);
        defer arena.deinit();
        const alloc = arena.allocator();

        const msg = try protocol.parseInbound(alloc, line);

        switch (msg) {
            .identify => |req| self.handleIdentify(req),
            .spawn_request => |req| self.handleSpawn(req),
            .subscribe_request => |req| self.handleSubscribe(req),
            .input => |req| self.handleInput(alloc, req),
            .resize_request => |req| self.handleResize(req),
            .list_regions_request => self.handleListRegions(alloc),
            .status_request => self.handleStatus(),
            .get_screen_request => |req| self.handleGetScreen(req),
            .kill_region_request => |req| self.handleKillRegion(req),
            .list_clients_request => self.handleListClients(alloc),
            .kill_client_request => |req| self.handleKillClient(req),
        }
    }

    fn handleIdentify(self: *Client, req: protocol.Identify) void {
        const new_hostname = self.alloc.dupe(u8, req.hostname) catch return;
        const new_username = self.alloc.dupe(u8, req.username) catch {
            self.alloc.free(new_hostname);
            return;
        };
        const new_process = self.alloc.dupe(u8, req.process) catch {
            self.alloc.free(new_hostname);
            self.alloc.free(new_username);
            return;
        };

        if (self.hostname_owned) self.alloc.free(self.hostname);
        if (self.username_owned) self.alloc.free(self.username);
        if (self.process_owned) self.alloc.free(self.process);

        self.hostname = new_hostname;
        self.hostname_owned = true;
        self.username = new_username;
        self.username_owned = true;
        self.client_pid = req.pid;
        self.process = new_process;
        self.process_owned = true;

        log.debug("client id={d} identified: {s}@{s} pid={d} process={s}", .{
            self.id, self.username, self.hostname, self.client_pid, self.process,
        });
    }

    fn handleSpawn(self: *Client, req: protocol.SpawnRequest) void {
        const region = self.server.spawnRegion(req.cmd, req.args) catch |err| {
            self.sendMessage(.{ .spawn_response = .{
                .region_id = "", .name = "",
                .@"error" = true, .message = @errorName(err),
            } });
            return;
        };

        self.sendMessage(.{ .spawn_response = .{
            .region_id = &region.id, .name = region.name,
            .@"error" = false, .message = "",
        } });

        // Intentionally broadcast to all clients including the spawning client.
        // The spawning client gets both spawn_response and region_created by design.
        self.server.broadcast(.{ .region_created = .{
            .region_id = &region.id, .name = region.name,
        } });
    }

    fn handleSubscribe(self: *Client, req: protocol.SubscribeRequest) void {
        if (req.region_id.len != 36) {
            self.sendMessage(.{ .subscribe_response = .{
                .region_id = req.region_id,
                .@"error" = true, .message = "invalid region_id length",
            } });
            return;
        }

        const region = self.server.findRegion(req.region_id) orelse {
            self.sendMessage(.{ .subscribe_response = .{
                .region_id = req.region_id,
                .@"error" = true, .message = "region not found",
            } });
            return;
        };

        self.subscribed_region_id = region.id;

        if (region.snapshot(self.alloc)) |snap| {
            defer {
                for (snap.lines) |l| self.alloc.free(l);
                self.alloc.free(snap.lines);
            }
            self.sendMessage(.{ .screen_update = .{
                .region_id = &region.id,
                .cursor_row = snap.cursor_row, .cursor_col = snap.cursor_col,
                .lines = snap.lines,
            } });
        } else |_| {}

        self.sendMessage(.{ .subscribe_response = .{
            .region_id = &region.id, .@"error" = false, .message = "",
        } });

        log.debug("client id={d} subscribed to region {s}", .{ self.id, &region.id });
    }

    fn handleInput(self: *Client, alloc: std.mem.Allocator, req: protocol.InputMsg) void {
        const region = self.server.findRegion(req.region_id) orelse return;
        const decoded_len = std.base64.standard.Decoder.calcSizeForSlice(req.data) catch return;
        const decoded = alloc.alloc(u8, decoded_len) catch return;
        defer alloc.free(decoded);
        std.base64.standard.Decoder.decode(decoded, req.data) catch return;
        region.writeInput(decoded) catch {};
    }

    fn handleResize(self: *Client, req: protocol.ResizeRequest) void {
        const region = self.server.findRegion(req.region_id) orelse {
            self.sendMessage(.{ .resize_response = .{
                .region_id = req.region_id,
                .@"error" = true, .message = "region not found",
            } });
            return;
        };

        region.resize(self.alloc, req.width, req.height) catch |err| {
            self.sendMessage(.{ .resize_response = .{
                .region_id = req.region_id,
                .@"error" = true, .message = @errorName(err),
            } });
            return;
        };

        self.sendMessage(.{ .resize_response = .{
            .region_id = &region.id, .@"error" = false, .message = "",
        } });
    }

    fn handleListRegions(self: *Client, alloc: std.mem.Allocator) void {
        const regions = &self.server.regions;
        var infos = alloc.alloc(protocol.RegionInfo, regions.count()) catch {
            self.sendMessage(.{ .list_regions_response = .{
                .regions = &.{}, .@"error" = true, .message = "out of memory",
            } });
            return;
        };

        var i: usize = 0;
        var it = regions.iterator();
        while (it.next()) |entry| {
            const r = entry.value_ptr.*;
            infos[i] = .{
                .region_id = entry.key_ptr.*,
                .name = r.name,
                .cmd = r.cmd,
                .pid = r.child_pid,
            };
            i += 1;
        }

        self.sendMessage(.{ .list_regions_response = .{
            .regions = infos, .@"error" = false, .message = "",
        } });
    }

    fn handleStatus(self: *Client) void {
        const now = std.time.timestamp();
        const uptime = now - self.server.start_time;
        self.sendMessage(.{ .status_response = .{
            .pid = @intCast(std.c.getpid()),
            .uptime_seconds = uptime,
            .socket_path = self.server.socket_path,
            .num_clients = @intCast(self.server.clients.items.len),
            .num_regions = @intCast(self.server.regions.count()),
            .@"error" = false, .message = "",
        } });
    }

    fn handleGetScreen(self: *Client, req: protocol.GetScreenRequest) void {
        const region = self.server.findRegion(req.region_id) orelse {
            self.sendMessage(.{ .get_screen_response = .{
                .region_id = req.region_id,
                .cursor_row = 0, .cursor_col = 0, .lines = &.{},
                .@"error" = true, .message = "region not found",
            } });
            return;
        };

        const snap = region.snapshot(self.alloc) catch {
            self.sendMessage(.{ .get_screen_response = .{
                .region_id = req.region_id,
                .cursor_row = 0, .cursor_col = 0, .lines = &.{},
                .@"error" = true, .message = "snapshot failed",
            } });
            return;
        };
        defer {
            for (snap.lines) |l| self.alloc.free(l);
            self.alloc.free(snap.lines);
        }

        self.sendMessage(.{ .get_screen_response = .{
            .region_id = &region.id,
            .cursor_row = snap.cursor_row, .cursor_col = snap.cursor_col,
            .lines = snap.lines,
            .@"error" = false, .message = "",
        } });
    }

    fn handleKillRegion(self: *Client, req: protocol.KillRegionRequest) void {
        if (self.server.killRegion(req.region_id)) {
            self.sendMessage(.{ .kill_region_response = .{
                .region_id = req.region_id, .@"error" = false, .message = "",
            } });
        } else {
            self.sendMessage(.{ .kill_region_response = .{
                .region_id = req.region_id, .@"error" = true, .message = "region not found",
            } });
        }
    }

    fn handleListClients(self: *Client, alloc: std.mem.Allocator) void {
        const clients = self.server.clients.items;
        var infos = alloc.alloc(protocol.ClientInfo, clients.len) catch {
            self.sendMessage(.{ .list_clients_response = .{
                .clients = &.{}, .@"error" = true, .message = "out of memory",
            } });
            return;
        };

        for (clients, 0..) |cl, i| {
            infos[i] = .{
                .client_id = cl.id,
                .hostname = cl.hostname,
                .username = cl.username,
                .pid = cl.client_pid,
                .process = cl.process,
                .subscribed_region_id = if (cl.subscribed_region_id) |sid| &sid else "",
            };
        }

        self.sendMessage(.{ .list_clients_response = .{
            .clients = infos, .@"error" = false, .message = "",
        } });
    }

    fn handleKillClient(self: *Client, req: protocol.KillClientRequest) void {
        if (req.client_id == self.id) {
            self.sendMessage(.{ .kill_client_response = .{
                .client_id = req.client_id, .@"error" = true, .message = "cannot kill self",
            } });
            return;
        }
        if (self.server.killClient(req.client_id)) {
            self.sendMessage(.{ .kill_client_response = .{
                .client_id = req.client_id, .@"error" = false, .message = "",
            } });
        } else {
            self.sendMessage(.{ .kill_client_response = .{
                .client_id = req.client_id, .@"error" = true, .message = "client not found",
            } });
        }
    }
};

const std = @import("std");
const protocol = @import("protocol.zig");
const Server = @import("server.zig").Server;
const Region = @import("region.zig").Region;

const log = std.log.scoped(.client);

pub const Client = struct {
    alloc: std.mem.Allocator,
    conn_fd: std.posix.fd_t,
    server: *Server,
    subscribed_region_id: ?[36]u8,
    read_buf: std.ArrayList(u8),

    pub fn init(alloc: std.mem.Allocator, conn_fd: std.posix.fd_t, server: *Server) *Client {
        const self = alloc.create(Client) catch @panic("OOM");
        self.* = .{
            .alloc = alloc,
            .conn_fd = conn_fd,
            .server = server,
            .subscribed_region_id = null,
            .read_buf = .{},
        };
        log.debug("client connected fd={d}", .{conn_fd});
        return self;
    }

    pub fn deinit(self: *Client) void {
        log.debug("client disconnected fd={d}", .{self.conn_fd});
        std.posix.close(self.conn_fd);
        self.read_buf.deinit(self.alloc);
        self.alloc.destroy(self);
    }

    /// Read available data from the socket. Returns false if the connection closed.
    pub fn readAvailable(self: *Client) !bool {
        var buf: [4096]u8 = undefined;
        const n = std.posix.read(self.conn_fd, &buf) catch |err| {
            log.debug("client read error: {}", .{err});
            return false;
        };
        if (n == 0) return false;

        try self.read_buf.appendSlice(self.alloc, buf[0..n]);

        // Process complete lines
        while (std.mem.indexOfScalar(u8, self.read_buf.items, '\n')) |nl| {
            const line = self.read_buf.items[0..nl];
            self.handleMessage(line) catch |err| {
                log.debug("message handling error: {}", .{err});
            };
            // Remove processed line + newline
            const rest = self.read_buf.items[nl + 1 ..];
            std.mem.copyForwards(u8, self.read_buf.items[0..rest.len], rest);
            self.read_buf.shrinkRetainingCapacity(rest.len);
        }

        return true;
    }

    /// Send an outbound message to this client.
    pub fn sendMessage(self: *Client, msg: protocol.OutboundMessage) void {
        var buf: [65536]u8 = undefined;
        var writer = std.io.Writer.fixed(&buf);
        protocol.writeOutbound(&writer, msg) catch |err| {
            log.debug("client send error fd={d}: {}", .{ self.conn_fd, err });
            return;
        };
        // Written data is in buf[0..writer.end]
        _ = std.posix.write(self.conn_fd, buf[0..writer.end]) catch {};
    }

    fn handleMessage(self: *Client, line: []const u8) !void {
        var arena = std.heap.ArenaAllocator.init(self.alloc);
        defer arena.deinit();
        const alloc = arena.allocator();

        const msg = try protocol.parseInbound(alloc, line);

        switch (msg) {
            .spawn_request => |req| self.handleSpawn(req),
            .subscribe_request => |req| self.handleSubscribe(req),
            .input => |req| self.handleInput(alloc, req),
            .resize_request => |req| self.handleResize(req),
        }
    }

    fn handleSpawn(self: *Client, req: protocol.SpawnRequest) void {
        const region = self.server.spawnRegion(req.cmd, req.args) catch |err| {
            self.sendMessage(.{ .spawn_response = .{
                .region_id = "",
                .name = "",
                .@"error" = true,
                .message = @errorName(err),
            } });
            return;
        };

        self.sendMessage(.{ .spawn_response = .{
            .region_id = &region.id,
            .name = region.name,
            .@"error" = false,
            .message = "",
        } });

        // Broadcast region_created to all clients
        self.server.broadcast(.{ .region_created = .{
            .region_id = &region.id,
            .name = region.name,
        } });
    }

    fn handleSubscribe(self: *Client, req: protocol.SubscribeRequest) void {
        if (req.region_id.len != 36) {
            self.sendMessage(.{ .subscribe_response = .{
                .region_id = req.region_id,
                .@"error" = true,
                .message = "invalid region_id length",
            } });
            return;
        }

        const region = self.server.findRegion(req.region_id) orelse {
            self.sendMessage(.{ .subscribe_response = .{
                .region_id = req.region_id,
                .@"error" = true,
                .message = "region not found",
            } });
            return;
        };

        self.subscribed_region_id = region.id;

        // Send initial screen_update before subscribe_response (per protocol spec)
        if (region.snapshot(self.alloc)) |snap| {
            defer {
                for (snap.lines) |l| self.alloc.free(l);
                self.alloc.free(snap.lines);
            }
            self.sendMessage(.{ .screen_update = .{
                .region_id = &region.id,
                .cursor_row = snap.cursor_row,
                .cursor_col = snap.cursor_col,
                .lines = snap.lines,
            } });
        } else |_| {}

        self.sendMessage(.{ .subscribe_response = .{
            .region_id = &region.id,
            .@"error" = false,
            .message = "",
        } });

        log.debug("client fd={d} subscribed to region {s}", .{ self.conn_fd, &region.id });
    }

    fn handleInput(self: *Client, alloc: std.mem.Allocator, req: protocol.InputMsg) void {
        const region = self.server.findRegion(req.region_id) orelse return;

        // Base64 decode
        const decoded_len = std.base64.standard.Decoder.calcSizeForSlice(req.data) catch {
            log.debug("base64 decode: invalid input length", .{});
            return;
        };
        const decoded = alloc.alloc(u8, decoded_len) catch return;
        defer alloc.free(decoded);
        std.base64.standard.Decoder.decode(decoded, req.data) catch |err| {
            log.debug("base64 decode error: {}", .{err});
            return;
        };

        region.writeInput(decoded) catch {};
    }

    fn handleResize(self: *Client, req: protocol.ResizeRequest) void {
        const region = self.server.findRegion(req.region_id) orelse {
            self.sendMessage(.{ .resize_response = .{
                .region_id = req.region_id,
                .@"error" = true,
                .message = "region not found",
            } });
            return;
        };

        region.resize(self.alloc, req.width, req.height) catch |err| {
            self.sendMessage(.{ .resize_response = .{
                .region_id = req.region_id,
                .@"error" = true,
                .message = @errorName(err),
            } });
            return;
        };

        self.sendMessage(.{ .resize_response = .{
            .region_id = &region.id,
            .@"error" = false,
            .message = "",
        } });
    }
};

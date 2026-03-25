const std = @import("std");
const log = std.log.scoped(.protocol);

// ── Outbound messages (server → frontend) ───────────────────────────────────

pub const SpawnResponse = struct {
    region_id: []const u8,
    name: []const u8,
    @"error": bool,
    message: []const u8,
};

pub const SubscribeResponse = struct {
    region_id: []const u8,
    @"error": bool,
    message: []const u8,
};

pub const ResizeResponse = struct {
    region_id: []const u8,
    @"error": bool,
    message: []const u8,
};

pub const RegionCreated = struct {
    region_id: []const u8,
    name: []const u8,
};

pub const ScreenUpdate = struct {
    region_id: []const u8,
    cursor_row: u16,
    cursor_col: u16,
    lines: []const []const u8,
};

pub const RegionDestroyed = struct {
    region_id: []const u8,
};

pub const OutboundMessage = union(enum) {
    spawn_response: SpawnResponse,
    subscribe_response: SubscribeResponse,
    resize_response: ResizeResponse,
    region_created: RegionCreated,
    screen_update: ScreenUpdate,
    region_destroyed: RegionDestroyed,
};

// ── Inbound messages (frontend → server) ────────────────────────────────────

pub const SpawnRequest = struct {
    cmd: []const u8,
    args: []const []const u8,
};

pub const SubscribeRequest = struct {
    region_id: []const u8,
};

pub const InputMsg = struct {
    region_id: []const u8,
    data: []const u8,
};

pub const ResizeRequest = struct {
    region_id: []const u8,
    width: u16,
    height: u16,
};

pub const InboundMessage = union(enum) {
    spawn_request: SpawnRequest,
    subscribe_request: SubscribeRequest,
    input: InputMsg,
    resize_request: ResizeRequest,
};

// ── Serialization ────────────────────────────────────────────────────────────

const TypeTag = struct {
    @"type": []const u8,
};

const json_opts: std.json.ParseOptions = .{
    .ignore_unknown_fields = true,
};

/// Parse one newline-delimited JSON line into an InboundMessage.
/// `alloc` should be an arena; parsed string data lives in it.
pub fn parseInbound(alloc: std.mem.Allocator, line: []const u8) !InboundMessage {
    const tag = try std.json.parseFromSliceLeaky(TypeTag, alloc, line, json_opts);
    log.debug("recv type={s}", .{tag.@"type"});

    if (std.mem.eql(u8, tag.@"type", "spawn_request")) {
        return .{ .spawn_request = try std.json.parseFromSliceLeaky(SpawnRequest, alloc, line, json_opts) };
    } else if (std.mem.eql(u8, tag.@"type", "subscribe_request")) {
        return .{ .subscribe_request = try std.json.parseFromSliceLeaky(SubscribeRequest, alloc, line, json_opts) };
    } else if (std.mem.eql(u8, tag.@"type", "input")) {
        return .{ .input = try std.json.parseFromSliceLeaky(InputMsg, alloc, line, json_opts) };
    } else if (std.mem.eql(u8, tag.@"type", "resize_request")) {
        return .{ .resize_request = try std.json.parseFromSliceLeaky(ResizeRequest, alloc, line, json_opts) };
    }

    return error.UnknownMessageType;
}

/// Serialize an OutboundMessage as a single JSON line + '\n'.
pub fn writeOutbound(writer: *std.io.Writer, msg: OutboundMessage) !void {
    switch (msg) {
        .spawn_response => |r| try writer.print("{f}", .{std.json.fmt(.{
            .@"type" = "spawn_response",
            .region_id = r.region_id,
            .name = r.name,
            .@"error" = r.@"error",
            .message = r.message,
        }, .{})}),
        .subscribe_response => |r| try writer.print("{f}", .{std.json.fmt(.{
            .@"type" = "subscribe_response",
            .region_id = r.region_id,
            .@"error" = r.@"error",
            .message = r.message,
        }, .{})}),
        .resize_response => |r| try writer.print("{f}", .{std.json.fmt(.{
            .@"type" = "resize_response",
            .region_id = r.region_id,
            .@"error" = r.@"error",
            .message = r.message,
        }, .{})}),
        .region_created => |r| try writer.print("{f}", .{std.json.fmt(.{
            .@"type" = "region_created",
            .region_id = r.region_id,
            .name = r.name,
        }, .{})}),
        .screen_update => |r| try writer.print("{f}", .{std.json.fmt(.{
            .@"type" = "screen_update",
            .region_id = r.region_id,
            .cursor_row = r.cursor_row,
            .cursor_col = r.cursor_col,
            .lines = r.lines,
        }, .{})}),
        .region_destroyed => |r| try writer.print("{f}", .{std.json.fmt(.{
            .@"type" = "region_destroyed",
            .region_id = r.region_id,
        }, .{})}),
    }
    try writer.writeByte('\n');
    switch (msg) {
        .screen_update => |r| log.debug("send type=screen_update cursor=({d},{d})", .{ r.cursor_row, r.cursor_col }),
        else => log.debug("send type={s}", .{@tagName(msg)}),
    }
}

const std = @import("std");
const Server = @import("server.zig").Server;

pub const std_options: std.Options = .{
    .log_level = .debug,
    .logFn = customLog,
};

var debug_enabled: bool = false;

fn customLog(
    comptime level: std.log.Level,
    comptime scope: @TypeOf(.enum_literal),
    comptime format: []const u8,
    args: anytype,
) void {
    if (!debug_enabled and level == .debug) return;

    const millis = std.time.milliTimestamp();
    const day_ms: u64 = @intCast(@mod(millis, 86400 * 1000));
    const hours = day_ms / 3600000;
    const minutes = (day_ms % 3600000) / 60000;
    const seconds = (day_ms % 60000) / 1000;
    const ms = day_ms % 1000;

    const level_str = comptime switch (level) {
        .debug => "debug",
        .info => "info ",
        .warn => "warn ",
        .err => "error",
    };

    _ = scope;
    var buf: [4096]u8 = undefined;
    const msg = std.fmt.bufPrint(&buf, "{d:0>2}:{d:0>2}:{d:0>2}.{d:0>3} {s} " ++ format ++ "\n", .{ hours, minutes, seconds, ms, level_str } ++ args) catch return;
    _ = std.posix.write(2, msg) catch {};
}

fn printUsage() void {
    const help =
        \\Usage: termd [options]
        \\
        \\Options:
        \\  -s, --socket <path>  Socket path (env: TERMD_SOCKET, default: /tmp/termd.sock)
        \\  -d, --debug          Enable debug logging (env: TERMD_DEBUG=1)
        \\  -h, --help           Show this help
        \\
    ;
    _ = std.posix.write(2, help) catch {};
}

pub fn main() !void {
    var gpa = std.heap.GeneralPurposeAllocator(.{}){};
    defer _ = gpa.deinit();
    const alloc = gpa.allocator();

    const args = try std.process.argsAlloc(alloc);
    defer std.process.argsFree(alloc, args);

    var socket_path: [:0]const u8 = "/tmp/termd.sock";
    var socket_flag_set = false;
    var i: usize = 1;
    while (i < args.len) : (i += 1) {
        const arg = args[i];
        if (std.mem.eql(u8, arg, "-h") or std.mem.eql(u8, arg, "--help")) {
            printUsage();
            return;
        } else if (std.mem.eql(u8, arg, "-d") or std.mem.eql(u8, arg, "--debug")) {
            debug_enabled = true;
        } else if (std.mem.eql(u8, arg, "-s") or std.mem.eql(u8, arg, "--socket")) {
            i += 1;
            if (i >= args.len) {
                _ = std.posix.write(2, "error: --socket requires a path argument\n") catch {};
                printUsage();
                return;
            }
            socket_path = args[i];
            socket_flag_set = true;
        } else {
            _ = std.posix.write(2, "error: unknown option: ") catch {};
            _ = std.posix.write(2, arg) catch {};
            _ = std.posix.write(2, "\n") catch {};
            printUsage();
            return;
        }
    }

    // Env var fallbacks
    if (!debug_enabled) {
        if (std.posix.getenv("TERMD_DEBUG")) |val| {
            if (std.mem.eql(u8, val, "1")) debug_enabled = true;
        }
    }
    if (!socket_flag_set) {
        if (std.posix.getenv("TERMD_SOCKET")) |val| {
            socket_path = val;
        }
    }

    var server = try Server.init(alloc, socket_path);
    defer server.deinit();
    try server.run();
}

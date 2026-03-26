# Milestone 5 Goals — Server Rewrite in Go

Replace the Zig server with a Go implementation. The entire project is now a single language.

## Motivation

- **Single language**: server, frontend, and termctl are all Go. One toolchain, one build system,
  shared protocol types, easier onboarding.
- **Simpler build**: no Zig dependency, no ghostty fetch, no C library linking. Just `go build`.
- **Shared code**: the server imports `termd/frontend/protocol` and `termd/frontend/log` directly
  instead of maintaining parallel type definitions.
- **VT library**: go-te (pure Go port of Python pyte) replaces ghostty-vt. Full terminal
  emulation with screen buffer, cursor tracking, resize, alternate buffer.
- **PTY management**: creack/pty replaces manual posix_openpt/grantpt/fork/exec.

## What changed

- `server/` is now Go (was Zig)
- Zig, ZLS, and ghostty-vt removed from the dev environment
- Makefile uses `go build` for all three binaries
- DESIGN.md updated to reflect Go + go-te stack
- All 19 e2e tests pass against the new server

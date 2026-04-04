# Admin Guide

## Live Upgrade

nxtermd supports zero-downtime live upgrades. The old server serializes all runtime state (regions, sessions, PTY file descriptors) to the new process over a Unix socket. The new process re-reads `server.toml` on startup, so configuration changes take effect immediately after a live upgrade.

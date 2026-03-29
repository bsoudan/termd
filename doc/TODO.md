# TODO

## Bugs

- Prefix key race: if ctrl+b and the following key arrive as separate RawInputMsgs (separate PTY reads), the second message can be processed before the CommandLayer push from the first one executes. The key gets forwarded to the shell instead of being handled as a prefix command. Currently masked in tests by combining both bytes into a single write, but the underlying TUI bug remains — real users typing ctrl+b then a key will hit this intermittently.

## Future milestones

- Single-command launch (frontend auto-starts server, or combined binary)
- Multiple simultaneous regions with tab switching
- Scrollback support
- Native program support (termd-aware programs that own their own screen state)
- Tags and placement (programs express layout preferences)
- Binary protocol (flatbuffers or msgpack after message shapes stabilize)
- Provide gateway to open web browser

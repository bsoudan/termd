# TODO

## Bugs

- Prefix key race: if ctrl+b and the following key arrive as separate RawInputMsgs (separate PTY reads), the second message can be processed before the CommandLayer push from the first one executes. The key gets forwarded to the shell instead of being handled as a prefix command. Currently masked in tests by combining both bytes into a single write, but the underlying TUI bug remains — real users typing ctrl+b then a key will hit this intermittently.

## Future milestones

- Single-command launch (frontend auto-starts server, or combined binary)
- Multiple simultaneous regions with tab switching
- Native program support (termd-aware programs that own their own screen state)
- Tags and placement (programs express layout preferences)
- Upgrade protocol: add an intermediate ack from the new process after it receives state but before full reconstruction completes, so the old process knows it's alive. This would allow keeping a tight timeout for the initial handshake while tolerating slow reconstruction of large state (currently worked around with a 60s flat timeout).
- Paginated scrollback: the get_scrollback_response sends the entire history in a single JSON line which can be multi-MB. Paginate it (e.g. request a range of lines) so the client can fetch incrementally and the message stays small.
- Binary protocol (flatbuffers or msgpack after message shapes stabilize)
- Provide gateway to open web browser

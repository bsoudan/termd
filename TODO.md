# TODO

## Scrollback sync blocks input after exit

When the user enters scrollback on a session with large server-side history (e.g., 10000 lines), the server streams `GetScrollbackResponse` chunks (~1000 lines each, ~300ms apart). If the user exits scrollback while chunks are still streaming, the remaining chunks sit in bubbletea's message queue ahead of subsequent `RawInputMsg` and `TerminalEvents` messages. This causes typed characters to not appear until all chunks have been processed.

Possible fixes:
- Process scrollback responses outside bubbletea's message loop (in the Server.Run goroutine) and only send a single completion message to bubbletea
- Prioritize input and terminal event messages over scrollback responses in the message queue
- Cancel the server-side scrollback stream when scrollback exits

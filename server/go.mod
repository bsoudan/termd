module termd/server

go 1.25.7

require (
	github.com/creack/pty v1.1.24
	github.com/rcarmo/go-te v0.1.0
	termd/frontend v0.0.0
)

replace termd/frontend => ../frontend

require (
	github.com/clipperhouse/stringish v0.1.1 // indirect
	github.com/clipperhouse/uax29/v2 v2.5.0 // indirect
	github.com/mattn/go-runewidth v0.0.19 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	golang.org/x/text v0.34.0 // indirect
)

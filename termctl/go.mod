module termd/termctl

go 1.25.7

replace termd/frontend => ../frontend

replace termd/transport => ../transport

require (
	github.com/urfave/cli/v2 v2.27.7
	termd/frontend v0.0.0-00010101000000-000000000000
	termd/transport v0.0.0
)

require (
	github.com/cpuguy83/go-md2man/v2 v2.0.7 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/xrash/smetrics v0.0.0-20240521201337-686a1a2994c1 // indirect
)

// Package dist holds distribution assets embedded into nxterm binaries
// at compile time. The TUI reads dist.Changelog for the "view recent
// changes" overlay; `make changelog` rewrites dist/changelog.txt from
// git log before each build.
package dist

import _ "embed"

//go:embed changelog.txt
var Changelog string

package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	te "nxtermd/pkg/te"
)

// TestTerminalLayerModes verifies that Modes() formats the set DEC
// private and ANSI modes correctly. Used by the status dialog.
func TestTerminalLayerModes(t *testing.T) {
	t.Run("nil screen returns empty", func(t *testing.T) {
		tl := &TerminalLayer{}
		if got := tl.Modes(); got != "" {
			t.Errorf("nil screen: got %q, want empty", got)
		}
	})

	t.Run("known DEC private modes", func(t *testing.T) {
		s := te.NewScreen(80, 24)
		// NewScreen sets DECAWM (7) and DECTCEM (25) by default.
		// Add bracketed paste (2004).
		s.SetMode([]int{2004}, true)
		tl := &TerminalLayer{screen: s}
		got := tl.Modes()
		for _, want := range []string{"DECAWM(autowrap)(7)", "DECTCEM(cursor-visible)(25)", "bracketed-paste(2004)"} {
			if !strings.Contains(got, want) {
				t.Errorf("Modes() = %q, want it to contain %q", got, want)
			}
		}
	})

	t.Run("cursor hidden does not include DECTCEM", func(t *testing.T) {
		s := te.NewScreen(80, 24)
		s.ResetMode([]int{25}, true)
		tl := &TerminalLayer{screen: s}
		got := tl.Modes()
		if strings.Contains(got, "DECTCEM") {
			t.Errorf("after \\e[?25l: Modes() = %q, must not contain DECTCEM", got)
		}
	})

	t.Run("unknown private mode", func(t *testing.T) {
		s := te.NewScreen(80, 24)
		s.SetMode([]int{9999}, true)
		tl := &TerminalLayer{screen: s}
		got := tl.Modes()
		if !strings.Contains(got, "?(9999)") {
			t.Errorf("Modes() = %q, want it to contain ?(9999)", got)
		}
	})
}

func TestSgrTransition(t *testing.T) {
	defaultAttr := te.Attr{}

	redFg := te.Attr{Fg: te.Color{Mode: te.ColorANSI16, Name: "red"}}
	greenFg := te.Attr{Fg: te.Color{Mode: te.ColorANSI16, Name: "green"}}

	boldOnly := te.Attr{Bold: true}
	boldRed := te.Attr{Bold: true, Fg: te.Color{Mode: te.ColorANSI16, Name: "red"}}
	boldGreen := te.Attr{Bold: true, Fg: te.Color{Mode: te.ColorANSI16, Name: "green"}}
	notBoldRed := te.Attr{Fg: te.Color{Mode: te.ColorANSI16, Name: "red"}}

	reverseUnderline := te.Attr{Reverse: true, Underline: true}
	reverseOnly := te.Attr{Reverse: true}

	italicOnly := te.Attr{Italics: true}

	tests := []struct {
		name         string
		from         te.Attr
		to           te.Attr
		want         string   // exact match if non-empty
		wantContains []string // substrings that must appear
		wantAbsent   []string // substrings that must NOT appear
	}{
		{
			name: "default to default",
			from: defaultAttr,
			to:   defaultAttr,
			want: ansi.ResetStyle, // reset is emitted because to == zero Attr
		},
		{
			name: "default to bold",
			from: defaultAttr,
			to:   boldOnly,
			want: ansi.SGR(ansi.AttrBold),
		},
		{
			name:         "default to red fg",
			from:         defaultAttr,
			to:           redFg,
			wantContains: []string{"31"},
		},
		{
			name: "bold+red to default",
			from: boldRed,
			to:   defaultAttr,
			want: ansi.ResetStyle,
		},
		{
			name:         "bold+red to bold+green",
			from:         boldRed,
			to:           boldGreen,
			wantContains: []string{"32"},
			wantAbsent:   []string{";1;", ";1m", "[1;"},
		},
		{
			name:         "bold+red to not-bold+red",
			from:         boldRed,
			to:           notBoldRed,
			wantContains: []string{"0", "31"},
		},
		{
			name:         "reverse+underline to reverse only",
			from:         reverseUnderline,
			to:           reverseOnly,
			wantContains: []string{"24"},
		},
		{
			name:         "italic to not-italic",
			from:         italicOnly,
			to:           defaultAttr,
			want:         "\x1b[m",
		},
		{
			name:         "italic to no attributes",
			from:         italicOnly,
			to:           greenFg,
			wantContains: []string{"23", "32"},
		},
		{
			name: "default to faint",
			from: defaultAttr,
			to:   te.Attr{Faint: true},
			want: ansi.SGR(ansi.AttrFaint),
		},
		{
			name:         "faint to bold (must reset)",
			from:         te.Attr{Faint: true},
			to:           te.Attr{Bold: true},
			wantContains: []string{"0", "1"},
		},
		{
			name: "faint to default",
			from: te.Attr{Faint: true},
			to:   defaultAttr,
			want: ansi.ResetStyle,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sgrTransition(tc.from, tc.to)

			if tc.want != "" {
				if got != tc.want {
					t.Errorf("sgrTransition() = %q, want %q", got, tc.want)
				}
				return
			}
			if tc.want == "" && len(tc.wantContains) == 0 && len(tc.wantAbsent) == 0 {
				if got != "" {
					t.Errorf("sgrTransition() = %q, want empty string", got)
				}
				return
			}

			for _, sub := range tc.wantContains {
				if !strings.Contains(got, sub) {
					t.Errorf("sgrTransition() = %q, want it to contain %q", got, sub)
				}
			}
			for _, sub := range tc.wantAbsent {
				if strings.Contains(got, sub) {
					t.Errorf("sgrTransition() = %q, want it NOT to contain %q", got, sub)
				}
			}
		})
	}
}

package tui

import (
	"reflect"
	"testing"
)

func TestExtractSyncMarkers(t *testing.T) {
	cases := []struct {
		name        string
		input       []byte
		wantRemain  []byte
		wantIDs     []string
	}{
		{
			name:       "single marker BEL terminator",
			input:      []byte("\x1b]2459;nx;sync;boot\x07"),
			wantRemain: []byte(""),
			wantIDs:    []string{"boot"},
		},
		{
			name:       "single marker ST terminator",
			input:      []byte("\x1b]2459;nx;sync;x\x1b\\"),
			wantRemain: []byte(""),
			wantIDs:    []string{"x"},
		},
		{
			name:       "marker with surrounding bytes",
			input:      []byte("hello\x1b]2459;nx;sync;id1\x07world"),
			wantRemain: []byte("helloworld"),
			wantIDs:    []string{"id1"},
		},
		{
			name:       "no marker",
			input:      []byte("hello world"),
			wantRemain: []byte("hello world"),
			wantIDs:    nil,
		},
		{
			name:       "two markers",
			input:      []byte("\x1b]2459;nx;sync;a\x07\x1b]2459;nx;sync;b\x07"),
			wantRemain: []byte(""),
			wantIDs:    []string{"a", "b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			remain, ids := ExtractSyncMarkers(tc.input)
			if string(remain) != string(tc.wantRemain) {
				t.Errorf("remaining mismatch: got %q, want %q", remain, tc.wantRemain)
			}
			if !reflect.DeepEqual(ids, tc.wantIDs) {
				t.Errorf("ids mismatch: got %v, want %v", ids, tc.wantIDs)
			}
		})
	}
}

func TestFormatSyncAck(t *testing.T) {
	got := FormatSyncAck("hello")
	want := "\x1b]2459;nx;ack;hello\x07"
	if got != want {
		t.Errorf("FormatSyncAck: got %q, want %q", got, want)
	}
}

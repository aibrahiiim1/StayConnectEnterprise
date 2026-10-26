package main

import (
	"strings"
	"testing"
)

func TestSeedAdminPasswordSources(t *testing.T) {
	cases := []struct {
		name, flag, stdin  string
		fromStdin, wantErr bool
		want               string
	}{
		{name: "flag only", flag: "from-the-flag-01", want: "from-the-flag-01"},
		{name: "stdin with newline", fromStdin: true, stdin: "pass phrase with spaces\nignored second line\n", want: "pass phrase with spaces"},
		{name: "stdin crlf", fromStdin: true, stdin: "windows-line-ending\r\n", want: "windows-line-ending"},
		{name: "stdin without newline", fromStdin: true, stdin: "no-trailing-newline", want: "no-trailing-newline"},
		{name: "stdin empty", fromStdin: true, stdin: "", wantErr: true},
		{name: "stdin blank line", fromStdin: true, stdin: "\n", wantErr: true},
		{name: "both sources", flag: "x", fromStdin: true, stdin: "y\n", wantErr: true},
	}
	for _, c := range cases {
		got, err := seedAdminPassword(c.flag, c.fromStdin, strings.NewReader(c.stdin))
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: want an error, got %q", c.name, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}

package agentout

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitize(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "go test ./...\tok", "go test ./...\tok"},
		{"sgr kept", "\x1b[1;31mred\x1b[0m", "\x1b[1;31mred\x1b[0m"},
		{"sgr normalized", "\x1b[01;031mx\x1b[m", "\x1b[1;31mx\x1b[0m"},
		{"blink and conceal dropped", "\x1b[5;8;32mx", "\x1b[32mx"},
		{"only unsafe codes", "\x1b[5mx\x1b[8m", "x"},
		{"256 colour", "\x1b[38;5;196mx", "\x1b[38;5;196mx"},
		{"truecolour", "\x1b[48;2;1;2;3mx", "\x1b[48;2;1;2;3mx"},
		{"bad extended colour stops", "\x1b[1;38;5;300;4mx", "\x1b[1mx"},
		{"oversized param", "\x1b[0000031mx", "x"},
		{"too many fields", "\x1b[" + strings.Repeat("1;", 40) + "1mx", "x"},
		{"osc title bel", "a\x1b]0;pwned title\x07b", "ab"},
		{"osc title st", "a\x1b]2;title\x1b\\b", "ab"},
		{"osc title c1 st", "a\x1b]2;title\u009cb", "ab"},
		{"osc hyperlink", "\x1b]8;;https://evil.example\x1b\\click\x1b]8;;\x1b\\", "click"},
		{"osc unterminated", "a\x1b]0;swallow the rest\nline", "a"},
		{"osc clipboard", "\x1b]52;c;cm0gLXJmIC8=\x07x", "x"},
		{"dcs", "\x1bPq#0;2;0;0;0\x1b\\x", "x"},
		{"apc pm sos", "\x1b_a\x1b\\\x1b^b\x1b\\\x1bXc\x1b\\x", "x"},
		{"cursor and erase", "a\x1b[2J\x1b[H\x1b[10;5Hb\x1b[K", "ab"},
		{"private mode", "\x1b[?1049h\x1b[?25lx", "x"},
		{"sgr with intermediate", "\x1b[1 mx", "x"},
		{"device status query", "\x1b[6nx\x1b[cy", "xy"},
		{"reset and keypad", "a\x1bcb\x1b=c\x1b7d", "abcd"},
		{"charset designation", "\x1b(Bx\x1b)0y", "xy"},
		{"c1 csi", "a\u009b31mb", "a31mb"},
		{"c0 and del", "a\x00b\x07c\x08d\x7fe\x0cf", "abcdef"},
		{"bidi overrides", "a\u202eb\u2066c\u200fd", "abcd"},
		{"lone esc at end", "a\x1b", "a"},
		{"double esc", "\x1b\x1b[31mx", "\x1b[31mx"},
		{"csi cut by newline", "\x1b[31\nx", "\nx"},
		{"csi cut by esc", "\x1b[31\x1b[32mx", "\x1b[32mx"},
		{"esc before non ascii", "\x1bé", "é"},
		{"carriage return kept", "a\rb", "a\rb"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Sanitize(c.in); got != c.want {
				t.Fatalf("Sanitize(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestClean(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		want  []string
		trunc bool
	}{
		{"crlf and trailing newline", "line1\r\nline2\n", []string{"line1", "line2"}, false},
		{"blank line kept", "a\n\nb", []string{"a", "", "b"}, false},
		{"progress bar", "10%\r50%\r100%\r\ndone", []string{"100%", "done"}, false},
		{"title only", "\x1b]0;title\x07", nil, false},
		{"invalid utf8", "a\xffb", []string{"a\uFFFDb"}, false},
		{"secret masked and colours dropped", "export GITHUB_TOKEN=\x1b[31mghp_abcdefghijklmnopqrstuvwxyz0123\x1b[0m",
			[]string{"export GITHUB_TOKEN=ghp_****"}, false},
		{"secret split by sgr", "key sk-ant-\x1b[1mabcdefghijklmnopqrstuvwx", []string{"key sk-a****"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, trunc := Clean(c.in, MaxChunk)
			if !slices.Equal(got, c.want) || trunc != c.trunc {
				t.Fatalf("Clean(%q) = %q %v, want %q %v", c.in, got, trunc, c.want, c.trunc)
			}
		})
	}

	got, trunc := Clean(strings.Repeat("é", 3000), MaxChunk)
	if !trunc || len(got) != 1 || len(got[0]) > MaxChunk || !utf8.ValidString(got[0]) || len(got[0]) != MaxChunk {
		t.Fatalf("truncation: %v, %d bytes, valid %v", trunc, len(got[0]), utf8.ValidString(got[0]))
	}
}

var sgrSeq = regexp.MustCompile(`^\x1b\[[0-9]+(;[0-9]+)*m`)

func checkClean(t *testing.T, s string) {
	t.Helper()
	if !utf8.ValidString(s) {
		t.Fatalf("invalid UTF-8 in %q", s)
	}
	for i, r := range s {
		switch {
		case r == esc:
			m := sgrSeq.FindString(s[i:])
			if m == "" {
				t.Fatalf("escape that is not SGR at %d in %q", i, s)
			}
			fields := strings.Split(m[2:len(m)-1], ";")
			for j := 0; j < len(fields); j++ {
				switch n, _ := strconv.Atoi(fields[j]); {
				case n == 38 || n == 48:
					if j+1 < len(fields) && fields[j+1] == "5" {
						j += 2
					} else {
						j += 4
					}
				case !allowedSGR(n):
					t.Fatalf("unsafe SGR %q in %q", m, s)
				}
			}
		case r < 0x20 && r != '\t' && r != '\n' && r != '\r', r == 0x7f, r >= 0x80 && r <= 0x9f:
			t.Fatalf("control %U at %d in %q", r, i, s)
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			t.Fatalf("bidi control %U in %q", r, s)
		}
	}
}

func FuzzClean(f *testing.F) {
	for _, s := range []string{
		"plain", "\x1b[1;31mx\x1b[0m", "\x1b]0;t\x07", "\x1b]8;;u\x1b\\l", "\x1bP\x1b\\", "\x1b[38;5;1;48;2;1;2;3m",
		"\x1b[?25l", "\u009b0m", "a\rb\r\n", "\x1b", "\x1b[", "\x1b[;;m", "token=abc\x1b[1m", "\xff\xfe",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		s := Sanitize(strings.ToValidUTF8(in, "\uFFFD"))
		checkClean(t, s)
		if again := Sanitize(s); again != s {
			t.Fatalf("not idempotent: %q -> %q", s, again)
		}
		lines, _ := Clean(in, MaxChunk)
		total := 0
		for _, l := range lines {
			checkClean(t, l)
			if strings.ContainsAny(l, "\r\n") {
				t.Fatalf("line break inside line %q", l)
			}
			total += len(l)
		}
		if total > 2*MaxChunk+len(lines) {
			t.Fatalf("cleaned output grew to %d bytes", total)
		}
	})
}

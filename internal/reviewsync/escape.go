package reviewsync

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/SigmaUno/subspace/internal/redact"
)

const (
	zwsp         = "\u200b"
	maxQuoteRows = 60
)

var (
	urlSchemeRe = regexp.MustCompile(`://`)
	wwwRe       = regexp.MustCompile(`(?i)\bwww\.`)
	ghRefRe     = regexp.MustCompile(`(?i)\b(gh)-(\d)`)
	leadOrdRe   = regexp.MustCompile(`^(\d+)\.`)
)

func clean(s string) string {
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(redact.String(strings.ToValidUTF8(s, "")))
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func escapeLine(s string) string {
	s = urlSchemeRe.ReplaceAllString(s, ":"+zwsp+"//")
	s = wwwRe.ReplaceAllString(s, "www"+zwsp+".")
	s = ghRefRe.ReplaceAllString(s, "$1-"+zwsp+"$2")
	var b strings.Builder
	b.Grow(len(s) + len(s)/4)
	for _, r := range s {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '@', '!':
			b.WriteRune(r)
			b.WriteString(zwsp)
		case '#':
			b.WriteString(`\#` + zwsp)
		case '\\', '`', '*', '_', '{', '}', '[', ']', '(', ')', '+', '-', '|', '~', '$':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return leadOrdRe.ReplaceAllString(b.String(), `$1\.`)
}

func truncate(s string, max int) (string, bool) {
	if utf8.RuneCountInString(s) <= max {
		return s, false
	}
	return string([]rune(s)[:max]), true
}

func inline(s string, max int) string {
	s, cut := truncate(strings.Join(strings.Fields(s), " "), max)
	s = escapeLine(s)
	if cut {
		s += "…"
	}
	return s
}

func quote(s string, max int) (string, bool) {
	s, cut := truncate(s, max)
	var rows []string
	blank := true
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.ReplaceAll(line, "\t", " "))
		if line == "" {
			if !blank {
				rows = append(rows, ">")
			}
			blank = true
			continue
		}
		blank = false
		if len(rows) == maxQuoteRows {
			cut = true
			break
		}
		rows = append(rows, "> "+escapeLine(line))
	}
	for len(rows) > 0 && rows[len(rows)-1] == ">" {
		rows = rows[:len(rows)-1]
	}
	return strings.Join(rows, "\n"), cut
}

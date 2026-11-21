package agentout

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/SigmaUno/subspace/internal/redact"
)

const (
	esc          = 0x1b
	maxSGRFields = 32
	maxCSIParams = 64
)

func Clean(text string, limit int) (lines []string, truncated bool) {
	if len(text) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text, truncated = text[:cut], true
	}
	styled := Sanitize(strings.ToValidUTF8(text, "\uFFFD"))
	if plain := StripSGR(styled); redact.Contains(plain) {
		styled = redact.String(plain)
	}
	if styled == "" {
		return nil, truncated
	}
	styled = strings.ReplaceAll(styled, "\r\n", "\n")
	styled = strings.TrimSuffix(styled, "\n")
	for _, l := range strings.Split(styled, "\n") {
		lines = append(lines, carriage(l))
	}
	return lines, truncated
}

func carriage(l string) string {
	if !strings.Contains(l, "\r") {
		return l
	}
	parts := strings.Split(l, "\r")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return ""
}

func Sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == esc {
			out, next := escape(s, i)
			b.WriteString(out)
			i = next
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		if keep(r) {
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}

func keep(r rune) bool {
	switch {
	case r == '\n' || r == '\t' || r == '\r':
		return true
	case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
		return false
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x200e || r == 0x200f || r == 0x061c:
		return false
	}
	return true
}

func escape(s string, i int) (string, int) {
	if i+1 >= len(s) {
		return "", len(s)
	}
	c := s[i+1]
	switch {
	case c == '[':
		return csi(s, i+2)
	case c == ']' || c == 'P' || c == 'X' || c == '^' || c == '_':
		return "", stringEnd(s, i+2)
	case c >= 0x20 && c <= 0x2f:
		j := i + 1
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
			j++
		}
		if j < len(s) && s[j] >= 0x30 && s[j] <= 0x7e {
			j++
		}
		return "", j
	case c >= 0x30 && c <= 0x7e:
		return "", i + 2
	}
	return "", i + 1
}

func csi(s string, start int) (string, int) {
	j := start
	for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
		j++
	}
	params := s[start:j]
	inter := j
	for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
		j++
	}
	if j >= len(s) || s[j] < 0x40 || s[j] > 0x7e {
		return "", j
	}
	if s[j] != 'm' || j != inter || len(params) > maxCSIParams {
		return "", j + 1
	}
	return sgr(params), j + 1
}

func stringEnd(s string, start int) int {
	for j := start; j < len(s); j++ {
		switch {
		case s[j] == 0x07:
			return j + 1
		case s[j] == esc && j+1 < len(s) && s[j+1] == '\\':
			return j + 2
		case s[j] == 0xc2 && j+1 < len(s) && s[j+1] == 0x9c:
			return j + 2
		}
	}
	return len(s)
}

func sgr(params string) string {
	if params == "" {
		return "\x1b[0m"
	}
	for _, c := range params {
		if (c < '0' || c > '9') && c != ';' {
			return ""
		}
	}
	fields := strings.Split(params, ";")
	if len(fields) > maxSGRFields {
		return ""
	}
	nums := make([]int, len(fields))
	for i, f := range fields {
		if len(f) > 3 {
			return ""
		}
		nums[i], _ = strconv.Atoi(f)
	}
	var kept []string
	for i := 0; i < len(nums); i++ {
		n := nums[i]
		switch {
		case n == 38 || n == 48:
			ext, used := extendedColor(nums[i+1:])
			if used == 0 {
				i = len(nums)
				continue
			}
			kept = append(kept, strconv.Itoa(n)+";"+ext)
			i += used
		case allowedSGR(n):
			kept = append(kept, strconv.Itoa(n))
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(kept, ";") + "m"
}

func extendedColor(rest []int) (string, int) {
	if len(rest) >= 2 && rest[0] == 5 && rest[1] <= 255 {
		return "5;" + strconv.Itoa(rest[1]), 2
	}
	if len(rest) >= 4 && rest[0] == 2 && rest[1] <= 255 && rest[2] <= 255 && rest[3] <= 255 {
		return "2;" + strconv.Itoa(rest[1]) + ";" + strconv.Itoa(rest[2]) + ";" + strconv.Itoa(rest[3]), 4
	}
	return "", 0
}

func allowedSGR(n int) bool {
	switch {
	case n <= 4, n == 7, n == 9, n >= 22 && n <= 24, n == 27, n == 29:
		return true
	case n >= 30 && n <= 37, n == 39, n >= 40 && n <= 47, n == 49:
		return true
	case n >= 90 && n <= 97, n >= 100 && n <= 107:
		return true
	}
	return false
}

func StripSGR(s string) string {
	if !strings.Contains(s, "\x1b[") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == esc && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

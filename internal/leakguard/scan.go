package leakguard

import (
	"encoding/base64"
	"slices"
	"strings"

	"github.com/SigmaUno/subspace/internal/auth"
)

const (
	secretBytes   = 32
	secretChars   = 43
	maxCandidates = 32
	marker        = "[redacted:token]"
)

var prefixes = [...]string{auth.TokenPrefix, auth.OwnerTokenPrefix}

type candidate struct {
	value string
	spans [][2]int
}

func candidates(text string) []*candidate {
	var out []*candidate
	var byValue map[string]*candidate
	for _, prefix := range prefixes {
		for from := 0; ; {
			i := strings.Index(text[from:], prefix)
			if i < 0 {
				break
			}
			start := from + i
			from = start + len(prefix)
			end := from + secretChars
			if end > len(text) || !wellFormed(text[from:end]) {
				continue
			}
			v := text[start:end]
			c := byValue[v]
			if c == nil {
				if len(out) == maxCandidates {
					continue
				}
				c = &candidate{value: v}
				if byValue == nil {
					byValue = map[string]*candidate{}
				}
				byValue[v] = c
				out = append(out, c)
			}
			c.spans = append(c.spans, [2]int{start, end})
			from = end
		}
	}
	return out
}

func wellFormed(s string) bool {
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return err == nil && len(b) == secretBytes
}

func mask(text string, c []*candidate) string {
	var spans [][2]int
	for _, x := range c {
		spans = append(spans, x.spans...)
	}
	slices.SortFunc(spans, func(a, b [2]int) int { return a[0] - b[0] })
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		if sp[0] < last {
			last = max(last, sp[1])
			continue
		}
		b.WriteString(text[last:sp[0]])
		b.WriteString(marker)
		last = sp[1]
	}
	b.WriteString(text[last:])
	return b.String()
}

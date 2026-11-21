// Package redact detects and masks secrets in free text.
//
// It is meant to be run over notes, messages, and other user-authored text
// before they are stored, so that tokens, keys, and credentials never persist
// in plaintext. Detection is heuristic and errs toward masking too much rather
// than too little. The generic high-entropy detector is configurable through
// the MinLength and MinEntropy package variables.
package redact

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// MinLength is the minimum run length (in bytes) for the generic high-entropy
// detector. Runs shorter than this are ignored by that detector (specific
// token detectors, such as "ghp_" or "sk-", are unaffected).
var MinLength = 32

// MinEntropy is the minimum Shannon entropy, in bits per character, required
// for the generic high-entropy detector to consider a run a secret.
var MinEntropy = 4.5

// mask is the literal replacement for the masked portion of a secret.
const mask = "****"

// group names the capture group that holds the secret itself. When empty, the
// whole match is treated as the secret.
type rule struct {
	re    *regexp.Regexp
	group string
	idx   int
	hint  func(s string, fold bool) bool
}

var rules = compile([]rule{
	// Private key PEM blocks (RSA, EC, OPENSSH, ENCRYPTED, ...).
	{re: regexp.MustCompile(`-----BEGIN [A-Z0-9 ]+PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]+PRIVATE KEY-----`), hint: has("-----BEGIN ")},
	// GitHub personal access tokens.
	{re: regexp.MustCompile(`\bgh[po]_[A-Za-z0-9]{20,}\b`), hint: has("ghp_", "gho_")},
	{re: regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`), hint: has("github_pat_")},
	// Anthropic, OpenAI, and DeepSeek-style "sk-" keys.
	{re: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{16,}`), hint: has("sk-ant-")},
	{re: regexp.MustCompile(`\bsk-proj-[A-Za-z0-9_-]{20,}`), hint: has("sk-proj-")},
	{re: regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), hint: has("sk-")},
	// AWS access key IDs.
	{re: regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), hint: has("AKIA")},
	// JWTs: three base64url segments separated by dots, header starts with "eyJ".
	{re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{2,}\.eyJ[A-Za-z0-9_-]{2,}\.[A-Za-z0-9_-]{2,}`), hint: has("eyJ")},
	// Bearer tokens: keep the "Bearer " prefix, mask the token.
	{re: regexp.MustCompile(`(?i)\bbearer[ \t]+(?P<secret>[-A-Za-z0-9._~+=/]{20,})`), group: "secret", hint: hasFold("bearer")},
	// "key=value" / "key: value" credential pairs: mask the value only. The key
	// may be prefixed (e.g. "db_password") but must not be part of a longer word.
	{re: regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])(?:password|passwd|secret|token|api_key|apikey|client_secret|access_token)[ \t]*[:=][ \t]*["']?(?P<secret>[^ \t,;"'\r\n]+)`), group: "secret",
		hint: assignment("passw", "secret", "token", "api")},
})

func compile(rs []rule) []rule {
	for i := range rs {
		rs[i].idx = -1
		if rs[i].group != "" {
			rs[i].idx = rs[i].re.SubexpIndex(rs[i].group)
		}
	}
	return rs
}

func has(needles ...string) func(string, bool) bool {
	return func(s string, _ bool) bool {
		for _, n := range needles {
			if strings.Contains(s, n) {
				return true
			}
		}
		return false
	}
}

func hasFold(needle string) func(string, bool) bool {
	return func(s string, fold bool) bool {
		return fold || containsFold(s, needle)
	}
}

func assignment(keys ...string) func(string, bool) bool {
	return func(s string, fold bool) bool {
		if !strings.ContainsAny(s, ":=") {
			return false
		}
		if fold {
			return true
		}
		for _, k := range keys {
			if containsFold(s, k) {
				return true
			}
		}
		return false
	}
}

func unicodeFold(s string) bool {
	return strings.Contains(s, "\u212a") || strings.Contains(s, "\u017f")
}

func containsFold(s, lower string) bool {
	n := len(lower)
	for i := 0; i+n <= len(s); i++ {
		j := 0
		for ; j < n; j++ {
			c := s[i+j]
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != lower[j] {
				break
			}
		}
		if j == n {
			return true
		}
	}
	return false
}

func isAlnum(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
}

func isRun(c byte) bool {
	return isAlnum(c) || c == '+' || c == '/' || c == '='
}

func genericRuns(s string, min int, out []span) []span {
	for i := 0; i < len(s); {
		if !isAlnum(s[i]) {
			i++
			continue
		}
		j := i + 1
		for j < len(s) && isRun(s[j]) {
			j++
		}
		if j-i >= min {
			out = append(out, span{i, j})
		}
		i = j
	}
	return out
}

type span struct{ lo, hi int }

// findSecrets returns the byte spans of every detected secret, sorted and
// merged so that overlapping detections are masked once.
func findSecrets(s string) []span {
	var out []span
	fold := unicodeFold(s)
	for i := range rules {
		r := &rules[i]
		if !r.hint(s, fold) {
			continue
		}
		for _, m := range r.re.FindAllStringSubmatchIndex(s, -1) {
			lo, hi := m[0], m[1]
			if r.idx >= 0 && 2*r.idx+1 < len(m) && m[2*r.idx] >= 0 {
				lo, hi = m[2*r.idx], m[2*r.idx+1]
			}
			if hi > lo {
				out = append(out, span{lo, hi})
			}
		}
	}
	n := len(out)
	out = genericRuns(s, max(MinLength, 8), out)
	k := n
	for _, sp := range out[n:] {
		sub := s[sp.lo:sp.hi]
		if isHex(sub) || shannon(sub) < MinEntropy {
			continue
		}
		out[k] = sp
		k++
	}
	return merge(out[:k])
}

func merge(spans []span) []span {
	if len(spans) < 2 {
		return spans
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].lo != spans[j].lo {
			return spans[i].lo < spans[j].lo
		}
		return spans[i].hi < spans[j].hi
	})
	out := spans[:1]
	for _, sp := range spans[1:] {
		last := &out[len(out)-1]
		if sp.lo <= last.hi {
			if sp.hi > last.hi {
				last.hi = sp.hi
			}
			continue
		}
		out = append(out, sp)
	}
	return out
}

// String returns s with every detected secret masked. Each secret keeps its
// first four characters (or is fully masked when shorter) and the remainder is
// replaced with asterisks.
func String(s string) string {
	spans := findSecrets(s)
	if len(spans) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(spans)*len(mask))
	last := 0
	for _, sp := range spans {
		b.WriteString(s[last:sp.lo])
		b.WriteString(masked(s[sp.lo:sp.hi]))
		last = sp.hi
	}
	b.WriteString(s[last:])
	return b.String()
}

// Contains reports whether s holds at least one detectable secret.
func Contains(s string) bool {
	return len(findSecrets(s)) > 0
}

// masked keeps a short identifying prefix and masks the rest. Secrets of four
// characters or fewer are masked entirely.
func masked(secret string) string {
	n := 0
	for i := range secret {
		if n == 4 {
			return secret[:i] + mask
		}
		n++
	}
	return mask
}

// shannon returns the Shannon entropy of s in bits per character.
func shannon(s string) float64 {
	var freq [256]int
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
	}
	n := float64(len(s))
	var h float64
	for _, c := range freq {
		if c == 0 {
			continue
		}
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h
}

// isHex reports whether s consists entirely of hexadecimal digits. It is used
// to leave git object SHAs untouched by the generic high-entropy detector.
func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

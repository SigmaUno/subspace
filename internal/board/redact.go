package board

import (
	"regexp"
	"strings"
)

type kindRule struct {
	kind string
	re   *regexp.Regexp
	hint []string
}

var kindRules = []kindRule{
	{"private_key", regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`), []string{"-----BEGIN "}},
	{"token", regexp.MustCompile(`\bss[pu]_[A-Za-z0-9_-]{16,}`), []string{"ssp_", "ssu_"}},
	{"provider_key", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-(?:ant-|proj-)?[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|xox[abprs]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{35})`),
		[]string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "sk-", "AKIA", "xox", "AIza"}},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{2,}\.eyJ[A-Za-z0-9_-]{2,}\.[A-Za-z0-9_-]{2,}`), []string{"eyJ"}},
}

var bearerRe = regexp.MustCompile(`(?i)(\bbearer[ \t]+)[-A-Za-z0-9._~+=/]{16,}`)

var passwordRe = regexp.MustCompile(`(?i)((?:^|[^A-Za-z0-9])[A-Za-z0-9_.-]*(?:password|passwd|pwd|secret|api[_-]?key|access[_-]?token|auth[_-]?token|token)["']?[ \t]*[:=][ \t]*)(?:"[^"\r\n]+"|'[^'\r\n]+'|[^\s,;"']{6,})`)

var passwordKeys = []string{"passw", "pwd", "secret", "api", "token"}

var markerRe = regexp.MustCompile(`\[redacted:[a-z_]+\]`)

func marker(kind string) string { return "[redacted:" + kind + "]" }

func Redact(s string) string {
	if s == "" {
		return s
	}
	for _, r := range kindRules {
		if containsAny(s, r.hint) {
			s = r.re.ReplaceAllString(s, marker(r.kind))
		}
	}
	fold := strings.Contains(s, "\u212a") || strings.Contains(s, "\u017f")
	if fold || containsFold(s, "bearer") {
		s = bearerRe.ReplaceAllString(s, "${1}"+marker("bearer"))
	}
	if !strings.ContainsAny(s, ":=") || !fold && !containsAnyFold(s, passwordKeys) {
		return s
	}
	return passwordRe.ReplaceAllStringFunc(s, func(m string) string {
		loc := passwordRe.FindStringSubmatchIndex(m)
		if markerRe.MatchString(m[loc[3]:]) {
			return m
		}
		return m[:loc[3]] + marker("password")
	})
}

func containsAny(s string, subs []string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

func containsAnyFold(s string, lowers []string) bool {
	for _, x := range lowers {
		if containsFold(s, x) {
			return true
		}
	}
	return false
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

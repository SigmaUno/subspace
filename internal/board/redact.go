package board

import "regexp"

type kindRule struct {
	kind string
	re   *regexp.Regexp
}

var kindRules = []kindRule{
	{"private_key", regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`)},
	{"token", regexp.MustCompile(`\bss[pu]_[A-Za-z0-9_-]{16,}`)},
	{"provider_key", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-(?:ant-|proj-)?[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|xox[abprs]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{35})`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{2,}\.eyJ[A-Za-z0-9_-]{2,}\.[A-Za-z0-9_-]{2,}`)},
}

var bearerRe = regexp.MustCompile(`(?i)(\bbearer[ \t]+)[-A-Za-z0-9._~+=/]{16,}`)

var passwordRe = regexp.MustCompile(`(?i)((?:^|[^A-Za-z0-9])[A-Za-z0-9_.-]*(?:password|passwd|pwd|secret|api[_-]?key|access[_-]?token|auth[_-]?token|token)["']?[ \t]*[:=][ \t]*)(?:"[^"\r\n]+"|'[^'\r\n]+'|[^\s,;"']{6,})`)

var markerRe = regexp.MustCompile(`\[redacted:[a-z_]+\]`)

func marker(kind string) string { return "[redacted:" + kind + "]" }

func Redact(s string) string {
	if s == "" {
		return s
	}
	for _, r := range kindRules {
		s = r.re.ReplaceAllString(s, marker(r.kind))
	}
	s = bearerRe.ReplaceAllString(s, "${1}"+marker("bearer"))
	s = passwordRe.ReplaceAllStringFunc(s, func(m string) string {
		loc := passwordRe.FindStringSubmatchIndex(m)
		if markerRe.MatchString(m[loc[3]:]) {
			return m
		}
		return m[:loc[3]] + marker("password")
	})
	return s
}

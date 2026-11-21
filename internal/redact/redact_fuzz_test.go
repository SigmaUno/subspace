package redact

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func FuzzString(f *testing.F) {
	for _, s := range []string{
		"", "plain text", "ghp_" + strings.Repeat("a1B2", 8), "sk-ant-" + strings.Repeat("x", 30), "AKIAABCDEFGHIJKLMNOP",
		"Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789",
		"password=hunter2hunter2", "db_password: \"pässwörd-ünïcode\"", "token=ä", "secret=🙂🙂🙂🙂🙂",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----",
		"-----BEGIN PRIVATE KEY-----\nnever closed",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk",
		strings.Repeat("aB3+/=", 40), strings.Repeat("password=x ", 50), "\xff\xfepassword=\xff\xfe",
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		start := time.Now()
		out := String(s)
		if el := time.Since(start); el > 5*time.Second {
			t.Fatalf("String took %s on %d bytes", el, len(s))
		}
		if len(out) > 4*len(s) {
			t.Fatalf("output grew from %d to %d bytes", len(s), len(out))
		}
		if utf8.ValidString(s) && !utf8.ValidString(out) {
			t.Fatalf("valid UTF-8 input produced invalid output %q", out)
		}
		if (out != s) != Contains(s) {
			t.Fatalf("Contains(%q) = %v but output changed = %v", s, Contains(s), out != s)
		}
		if again := String(out); again != out {
			t.Fatalf("not idempotent:\n%q\n%q", out, again)
		}
	})
}

func FuzzPlantedSecretsRemoved(f *testing.F) {
	f.Add("before", "after", "Zk3Qx9Lm2Pw8Vb7Nc4Hd6Jr5Ts1Yf0GaBcDeFgHiJk")
	f.Add("password=", "", "ünï")
	f.Add("", "\r\n", "0")
	f.Add("-----BEGIN RSA PRIVATE KEY-----", "-----END RSA PRIVATE KEY-----", "x")
	f.Fuzz(func(t *testing.T, prefix, suffix, seed string) {
		var b strings.Builder
		for _, r := range seed {
			b.WriteByte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"[int(r)%62])
		}
		body := b.String()
		body += strings.Repeat("Q7", 24)
		hidden := body[8:]
		plants := []string{
			"ghp_" + body,
			"github_pat_" + body,
			"sk-ant-" + body,
			"sk-proj-" + body,
			"sk-" + body,
			"AKIA" + strings.ToUpper(body)[:16],
			"eyJ" + body + ".eyJ" + body + "." + body,
			"Bearer " + body,
			"-----BEGIN RSA PRIVATE KEY-----\n" + body + "\n-----END RSA PRIVATE KEY-----",
			"password=" + body,
			"api_key: " + body,
		}
		for _, p := range plants {
			in := prefix + "\n" + p + "\n" + suffix
			out := String(in)
			if strings.Contains(prefix+suffix, hidden) {
				continue
			}
			if strings.Contains(out, hidden) {
				t.Fatalf("secret survived in %q\n-> %q", in, out)
			}
			if !Contains(in) {
				t.Fatalf("Contains missed %q", p)
			}
		}
	})
}

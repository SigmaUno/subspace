package redact

import "testing"

func TestStringMasksSecrets(t *testing.T) {
	const (
		ghp    = "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"
		gho    = "gho_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"
		pat    = "github_pat_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_abcdefghijklmnopqrstuvwxyz0123456789"
		ant    = "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcdefgh"
		proj   = "sk-proj-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcdef"
		openai = "sk-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcdefgh"
		deep   = "sk-0123456789abcdef0123456789abcdef"
		random = "9n2mKp4Rt8Qw3Xy5Zc7Vb1Nm6Lk0Jh3Gf5Ds9Aq2We4"
		jwt    = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	)

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"github ghp token", ghp, "ghp_****"},
		{"github gho token", gho, "gho_****"},
		{"github fine-grained pat", pat, "gith****"},
		{"anthropic key", ant, "sk-a****"},
		{"openai project key", proj, "sk-p****"},
		{"openai legacy key", openai, "sk-A****"},
		{"deepseek style key", deep, "sk-0****"},
		{"aws access key id", "AKIAIOSFODNN7EXAMPLE", "AKIA****"},
		{"pem block", "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAq8x9zF3y\n-----END RSA PRIVATE KEY-----", "----****"},
		{"jwt", jwt, "eyJh****"},
		{"bearer token", "Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ", "Authorization: Bearer abcd****"},
		{"password pair", "db_password=hunter2", "db_password=hunt****"},
		{"short password fully masked", "password=ab", "password=****"},
		{"generic high entropy", random, "9n2m****"},
		{"secret inside sentence", "the key is " + ghp + " keep it safe", "the key is ghp_**** keep it safe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := String(tc.in); got != tc.want {
				t.Errorf("String(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if !Contains(tc.in) {
				t.Errorf("Contains(%q) = false, want true", tc.in)
			}
		})
	}
}

func TestStringFalsePositives(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"git sha", "commit a3f5c2e1b4d6f7a8c9d0e1f2a3b4c5d6e7f8a9b0 done"},
		{"uppercase sha", "A3F5C2E1B4D6F7A8C9D0E1F2A3B4C5D6E7F8A9B0"},
		{"uuid", "id 550e8400-e29b-41d4-a716-446655440000 ok"},
		{"short hex", "deadbeef"},
		{"short tokenish word", "sk-ant"},
		{"plain prose", "the quick brown fox jumps over the lazy dog"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := String(tc.in); got != tc.in {
				t.Errorf("String(%q) = %q, want unchanged", tc.in, got)
			}
			if Contains(tc.in) {
				t.Errorf("Contains(%q) = true, want false", tc.in)
			}
		})
	}
}

func TestConfigurableThreshold(t *testing.T) {
	const random = "9n2mKp4Rt8Qw3Xy5Zc7Vb1Nm6Lk0Jh3Gf5Ds9Aq2We4"

	origLen, origEntropy := MinLength, MinEntropy
	t.Cleanup(func() {
		MinLength, MinEntropy = origLen, origEntropy
	})

	if !Contains(random) {
		t.Fatalf("default threshold should flag %q", random)
	}

	MinLength = len(random) + 1
	if Contains(random) {
		t.Errorf("with MinLength=%d, %q should not be flagged", MinLength, random)
	}
	MinLength = origLen

	MinEntropy = 100.0
	if Contains(random) {
		t.Errorf("with MinEntropy=100, %q should not be flagged", random)
	}
}

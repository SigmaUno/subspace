package board

import "testing"

func TestRedactKinds(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ssp_" + "Zk3Qm9vT1xYb7LwR2nHd5CpA8sGf4JuE0iKoX6tNyBc", "[redacted:token]"},
		{"Authorization: Bearer q8Zr2LmN5vTx9KbW3cYd7HjF1gPs4AeU", "Authorization: Bearer [redacted:bearer]"},
		{"key sk-ant-" + "api03-Qw8Er2Ty6Ui0Op4As9Df3Gh7Jk1Lz5X done", "key [redacted:provider_key] done"},
		{"ghp_" + "Qw8Er2Ty6Ui0Op4As9Df3Gh7Jk1Lz5Xc9Vb", "[redacted:provider_key]"},
		{"AKIA" + "QWERTYUIOPASDFGH", "[redacted:provider_key]"},
		{"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----\nafter", "[redacted:private_key]\nafter"},
		{"db_password=hunter2hunter2 next", "db_password=[redacted:password] next"},
		{`"api_key": "abc123"`, `"api_key": [redacted:password]`},
		{"SECRET: s3cr3t-value!", "SECRET: [redacted:password]"},
	}
	for _, c := range cases {
		if got := Redact(c.in); got != c.want {
			t.Errorf("Redact(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRedactLeavesBenignText(t *testing.T) {
	for _, in := range []string{
		"",
		"commit a3f5c2e1b4d6f7a8c9d0e1f2a3b4c5d6e7f8a9b0 merged",
		"id 550e8400-e29b-41d4-a716-446655440000",
		"PR https://git.example/o/r/pulls/12 on branch sub/t-3-add-login",
		"edited internal/board/redact.go and internal/board/comms.go",
		"token budget exceeded; tokens: 1200",
		"token: none",
		"password reset flow works; rotate the secret santa list",
		"Bearer tokens are required on /mcp",
		"sk-ant is the prefix, ssp_ too",
		"TestRedactLeavesBenignTextForVeryLongFunctionNames passed",
		"Tests: 12 passed, 0 failed\nok  \tgithub.com/SigmaUno/subspace/internal/board\t1.2s",
	} {
		if got := Redact(in); got != in {
			t.Errorf("Redact(%q) changed to %q", in, got)
		}
	}
}

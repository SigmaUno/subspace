package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnerTokenNotInheritedByChildProcesses(t *testing.T) {
	const env = "owner-token-from-env-0123456789"
	const file = "owner-token-from-file-0123456789"
	path := filepath.Join(t.TempDir(), "owner")
	if err := os.WriteFile(path, []byte(file+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, file, want string
	}{
		{"env", "", env},
		{"file", path, file},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SUBSPACE_OWNER_TOKEN", env)
			t.Setenv("SUBSPACE_OWNER_TOKEN_FILE", tc.file)
			tok, err := ownerToken()
			if err != nil {
				t.Fatal(err)
			}
			if tok != tc.want {
				t.Fatalf("token = %q, want %q", tok, tc.want)
			}
			out, err := exec.Command("env").Output()
			if err != nil {
				t.Skip("env not available:", err)
			}
			if strings.Contains(string(out), env) {
				t.Fatal("owner token leaked into the environment of child processes")
			}
		})
	}
}

package main

import (
	"errors"
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

func TestMainHelper(t *testing.T) {
	if os.Getenv("SUBSPACE_TEST_RUN_MAIN") != "1" {
		t.Skip("helper process")
	}
	os.Args = []string{"subspace"}
	main()
}

func TestUnknownSpawnerStopsStartup(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainHelper$")
	cmd.Env = append(os.Environ(),
		"SUBSPACE_TEST_RUN_MAIN=1",
		"SUBSPACE_OWNER_TOKEN=owner-token-for-spawner-test-0123456789",
		"SUBSPACE_DB="+filepath.Join(t.TempDir(), "subspace.db"),
		"SUBSPACE_ADDR=127.0.0.1:0",
		"SUBSPACE_SPAWNER=k8s",
	)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 || !strings.Contains(string(out), "unknown spawner") {
		t.Fatalf("err=%v output=%s", err, out)
	}
}

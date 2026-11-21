package server

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/backup/offsite"
)

func offsiteEnv(t *testing.T) (dest string) {
	t.Helper()
	dest = t.TempDir()
	key := filepath.Join(t.TempDir(), "backup.key")
	if err := os.WriteFile(key, []byte(strings.Repeat("cd", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUBSPACE_OFFSITE_KIND", "fs")
	t.Setenv("SUBSPACE_OFFSITE_PATH", dest)
	t.Setenv("SUBSPACE_OFFSITE_KEY_FILE", key)
	t.Setenv("SUBSPACE_OFFSITE_INTERVAL", "50ms")
	return dest
}

func TestBackupIsUploadedEncryptedAndReportedOnTheList(t *testing.T) {
	dest := offsiteEnv(t)
	e, _ := newBackupEnv(t, 5)
	if code, out := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "b", "body": "x"}); code != 201 {
		t.Fatalf("brief: %d %v", code, out)
	}
	code, out := e.rest("POST", "/api/v1/backup", map[string]any{})
	if code != 200 {
		t.Fatalf("backup: %d %v", code, out)
	}
	var remote map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, out = e.rest("GET", "/api/v1/backups", nil)
		remote, _ = out["backups"].([]any)[0].(map[string]any)["remote"].(map[string]any)
		if remote != nil && remote["verified"] == true {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if remote == nil || remote["verified"] != true || !strings.HasSuffix(remote["object"].(string), ".db.enc") || remote["error"] != "" {
		t.Fatalf("backup list never showed a verified remote copy: %v", out)
	}
	raw, err := os.ReadFile(filepath.Join(dest, remote["object"].(string)))
	if err != nil || bytes.Contains(raw, []byte("SQLite format")) {
		t.Fatalf("remote object is readable or missing: %v", err)
	}
	state, err := offsite.LoadState(context.Background(), e.srv.Store)
	if err != nil || len(state.Records) != 1 || state.LastError != "" {
		t.Fatalf("state %+v %v", state, err)
	}
}

func TestInvalidOffsiteConfigStopsStartup(t *testing.T) {
	t.Setenv("SUBSPACE_OFFSITE_KIND", "s3")
	t.Setenv("SUBSPACE_OFFSITE_S3_ENDPOINT", "https://10.0.0.1")
	dir := t.TempDir()
	_, err := New(context.Background(), Config{
		DBPath: filepath.Join(dir, "t.db"), OwnerToken: ownerTok, PublicURL: "http://sub.test",
	})
	if err == nil || !strings.Contains(err.Error(), "off-site backups") {
		t.Fatalf("got %v", err)
	}
}

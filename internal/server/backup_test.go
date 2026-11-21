package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newBackupEnv(t *testing.T, keep int) (*env, string) {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := New(ctx, Config{
		DBPath: filepath.Join(dir, "t.db"), BackupDir: filepath.Join(dir, "bk"), BackupKeep: keep,
		OwnerToken: ownerTok, PublicURL: "http://sub.test",
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute,
		Now: func() time.Time { return time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	go s.Board.Run(ctx)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); cancel(); _ = s.Close() })
	return &env{t: t, ts: ts, srv: s}, filepath.Join(dir, "bk")
}

func TestBackupEndpointsVerifyAndPrune(t *testing.T) {
	e, dir := newBackupEnv(t, 2)
	if code, out := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "b", "body": "x"}); code != 201 {
		t.Fatalf("brief: %d %v", code, out)
	}
	for _, n := range []string{"subspace-20200101T000000Z.db", "subspace-20200102T000000Z.db"} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, n), []byte("not a database"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	code, out := e.rest("POST", "/api/v1/backup", map[string]any{})
	if code != 200 || out["bytes"].(float64) <= 0 {
		t.Fatalf("backup: %d %v", code, out)
	}
	path := out["path"].(string)
	if filepath.Dir(path) != dir {
		t.Fatalf("path %q outside %q", path, dir)
	}

	if code, out := e.rest("POST", "/api/v1/backup", map[string]any{}); code != 409 {
		t.Fatalf("same-second backup: %d %v", code, out)
	}

	code, out = e.rest("GET", "/api/v1/backups", nil)
	if code != 200 {
		t.Fatalf("list: %d %v", code, out)
	}
	list := out["backups"].([]any)
	if len(list) != 2 {
		t.Fatalf("retention: %v", list)
	}
	newest, older := list[0].(map[string]any), list[1].(map[string]any)
	if newest["path"] != path || newest["verified"] != true || newest["bytes"].(float64) <= 0 {
		t.Fatalf("newest: %v", newest)
	}
	if older["name"] != "subspace-20200102T000000Z.db" || older["verified"] != false || older["age_seconds"].(float64) <= 0 {
		t.Fatalf("older: %v", older)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data[:len(data)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	_, out = e.rest("GET", "/api/v1/backups", nil)
	if out["backups"].([]any)[0].(map[string]any)["verified"] != false {
		t.Fatalf("truncated backup still verified: %v", out)
	}
}

func TestBackupEndpointsRequireOwner(t *testing.T) {
	e, _ := newBackupEnv(t, 2)
	agent := e.register("w1", "worker", "claude-code")
	for _, c := range []struct{ method, path string }{{"GET", "/api/v1/backups"}, {"POST", "/api/v1/backup"}} {
		for tok, want := range map[string]int{"": 401, agent: 403} {
			req, _ := http.NewRequest(c.method, e.ts.URL+c.path, nil)
			if tok != "" {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != want {
				t.Fatalf("%s %s with %q: %d, want %d", c.method, c.path, tok, resp.StatusCode, want)
			}
		}
	}
}

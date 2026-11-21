package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/backup"
	"github.com/SigmaUno/subspace/internal/backup/offsite"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

type listing []backup.Info

func (l listing) List(context.Context) ([]backup.Info, error) { return l, nil }

func offsiteBackup(t *testing.T) (dest, dir string, want map[string]int) {
	t.Helper()
	dir = t.TempDir()
	dest = t.TempDir()
	src := filepath.Join(dir, "live.db")
	st, err := sqlite.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	seed(t, st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 11, 21, 3, 0, 0, 0, time.UTC)
	bak := filepath.Join(dir, "backups", "subspace-20261121T030000Z.db")
	if err := runBackup([]string{"--db", src, "--out", bak}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	want = tableCounts(t, openRaw(t, bak))

	key := filepath.Join(dir, "backup.key")
	if err := os.WriteFile(key, []byte(strings.Repeat("ef", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUBSPACE_OFFSITE_KIND", "fs")
	t.Setenv("SUBSPACE_OFFSITE_PATH", dest)
	t.Setenv("SUBSPACE_OFFSITE_KEY_FILE", key)
	cfg, err := offsite.ConfigFromEnv(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := offsite.Open(cfg, offsite.Deps{Getenv: os.Getenv})
	if err != nil {
		t.Fatal(err)
	}
	local, err := sqlite.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	svc, err := offsite.NewService(offsite.Options{
		Remote: remote, Store: local, StageDir: filepath.Join(dir, "stage"),
		Backups: listing{{Name: filepath.Base(bak), Path: bak, Verified: true, CreatedAt: created}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	return dest, dir, want
}

func TestRestoreFromRemoteRoundTrip(t *testing.T) {
	_, dir, want := offsiteBackup(t)
	dst := filepath.Join(dir, "new", "subspace.db")
	var out bytes.Buffer
	if err := runRestore([]string{"--from-remote", "--db", dst}, &out); err != nil {
		t.Fatal(err)
	}
	if got := tableCounts(t, openRaw(t, dst)); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored counts %v, want %v", got, want)
	}
	if !strings.Contains(out.String(), "subspace-20261121T030000Z.db.enc") {
		t.Fatalf("output %q", out.String())
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "new", ".restore-remote-*")); len(left) != 0 {
		t.Fatalf("temporary download left behind: %v", left)
	}

	if err := runRestore([]string{"--from-remote", "--db", dst}, &out); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("overwrite without --force: %v", err)
	}
	if err := runRestore([]string{"--from-remote", "--name", "subspace-20261121T030000Z.db.enc", "--db", dst, "--force"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := runRestore([]string{"--from-remote", "--name", "subspace-19990101T000000Z.db.enc", "--db", dst, "--force"}, &out); err == nil {
		t.Fatal("unknown remote object restored")
	}
}

func TestRestoreFromRemoteRefusesTamperedBackup(t *testing.T) {
	dest, dir, _ := offsiteBackup(t)
	obj := filepath.Join(dest, "subspace-20261121T030000Z.db.enc")
	raw, err := os.ReadFile(obj)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 1
	if err := os.WriteFile(obj, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "restored", "subspace.db")
	err = runRestore([]string{"--from-remote", "--db", dst}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "authentication") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("tampered backup produced a database")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "restored", ".restore-remote-*")); len(left) != 0 {
		t.Fatalf("temporary download left behind: %v", left)
	}
}

func TestRestoreFromRemoteNeedsConfiguration(t *testing.T) {
	t.Setenv("SUBSPACE_OFFSITE_KIND", "")
	err := runRestore([]string{"--from-remote", "--db", filepath.Join(t.TempDir(), "x.db")}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("got %v", err)
	}
}

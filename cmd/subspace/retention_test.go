package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/retention"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

func TestRetentionRestoreCommand(t *testing.T) {
	dir := t.TempDir()
	path, arch := filepath.Join(dir, "subspace.db"), filepath.Join(dir, "retention")
	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		ev := &model.AuditEvent{TS: ts.Add(time.Duration(i) * 24 * time.Hour), AgentID: "w-1", Role: "worker", Tool: "heartbeat", After: map[string]any{"n": i}}
		if err := st.Atomic(context.Background(), func(tx store.Tx) error { return tx.InsertAudit(ev) }); err != nil {
			t.Fatal(err)
		}
	}
	pol := retention.DefaultPolicy()
	pol.AuditDays, pol.SegmentRows = 5, 4
	svc, err := retention.New(retention.Options{Store: st, Policy: pol, Dir: arch, Now: func() time.Time { return ts.Add(20 * 24 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if rep.Rows[retention.Audit] != 15 {
		t.Fatalf("archived %v", rep.Rows)
	}

	var out bytes.Buffer
	if err := runAuditVerify([]string{"--db", path}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "ok 9 events") || !strings.Contains(out.String(), "archive ok 4 segments 15 rows anchor 15 ") {
		t.Fatalf("audit-verify output %q", out.String())
	}

	seg := filepath.Join(arch, "audit", "audit-00000002.ndjson.gz")
	good, err := os.ReadFile(seg)
	if err != nil {
		t.Fatal(err)
	}
	bad := bytes.Clone(good)
	bad[len(bad)/2] ^= 0xff
	if err := os.WriteFile(seg, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runAuditVerify([]string{"--db", path}, &out); err == nil || !strings.Contains(err.Error(), "archived segment") {
		t.Fatalf("tampered segment: %v", err)
	}
	if err := os.WriteFile(seg, good, 0o600); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := runRetentionRestore([]string{"--db", path, "--kind", "audit"}, &out); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out.String(), "restored segment"); n != 4 {
		t.Fatalf("restore output %q", out.String())
	}
	out.Reset()
	if err := runAuditVerify([]string{"--db", path}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "ok 28 events") || strings.Contains(out.String(), "archive") {
		t.Fatalf("after restore %q", out.String())
	}
}

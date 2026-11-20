package main

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

func TestAuditVerifyCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subspace.db")
	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 11, 20, 21, 0, 0, 0, time.UTC)
	var last *model.AuditEvent
	for i := 0; i < 4; i++ {
		last = &model.AuditEvent{TS: ts.Add(time.Duration(i) * time.Second), AgentID: "w-1", Role: "worker", Tool: "heartbeat",
			After: map[string]any{"n": i}}
		if err := st.Atomic(context.Background(), func(tx store.Tx) error { return tx.InsertAudit(last) }); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runAuditVerify([]string{"--db", path}, &out); err != nil {
		t.Fatal(err)
	}
	if want := "ok 4 events head 4 " + last.Hash + "\n"; out.String() != want {
		t.Fatalf("output %q, want %q", out.String(), want)
	}

	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DROP TRIGGER audit_no_delete; DELETE FROM audit WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	err = runAuditVerify([]string{"--db", path}, &out)
	if err == nil || !strings.Contains(err.Error(), "broken at id 3") {
		t.Fatalf("tampered db: %v", err)
	}
}

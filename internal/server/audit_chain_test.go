package server_test

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/audit"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type exportLine struct {
	Chain *struct {
		Since    int64           `json:"since"`
		PrevHash string          `json:"prev_hash"`
		Head     model.AuditHead `json:"head"`
	} `json:"chain"`
	model.AuditEvent
}

func exportAudit(t *testing.T, e *servertest.Env, since int64) (exportLine, []model.AuditEvent) {
	t.Helper()
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/audit/export?since=%d", e.TS.URL, since), nil)
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("export: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var (
		head   exportLine
		events []model.AuditEvent
	)
	for i := 0; sc.Scan(); i++ {
		var l exportLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if (i == 0) != (l.Chain != nil) {
			t.Fatalf("line %d: chain header misplaced: %s", i, sc.Text())
		}
		if i == 0 {
			head = l
			continue
		}
		events = append(events, l.AuditEvent)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return head, events
}

func relink(t *testing.T, prev string, events []model.AuditEvent) string {
	t.Helper()
	text := func(m map[string]any) string {
		if m == nil {
			return ""
		}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	for _, ev := range events {
		h, err := audit.Link(prev, audit.Record{ID: ev.ID, TS: ev.TS.UnixMilli(), AgentID: ev.AgentID, Role: ev.Role, Tool: ev.Tool,
			TaskID: deref(ev.TaskID), Before: text(ev.Before), After: text(ev.After), Note: deref(ev.Note)})
		if err != nil {
			t.Fatal(err)
		}
		if h != ev.Hash {
			t.Fatalf("event %d: exported hash %s, recomputed %s", ev.ID, ev.Hash, h)
		}
		prev = h
	}
	return prev
}

func TestAuditVerifyAndExport(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	e := servertest.Start(t, server.Config{DBPath: dbPath, PublicURL: "http://sub.test",
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})
	for i := 0; i < 12; i++ {
		e.Register(fmt.Sprintf("w-%02d", i), "worker", "claude-code")
		if code, out := e.REST("POST", "/api/v1/briefs", map[string]any{"title": fmt.Sprintf("B <%d> & co", i), "repos": []string{"o/r"}}); code != 201 {
			t.Fatalf("brief: %d %v", code, out)
		}
	}

	code, chk := e.REST("GET", "/api/v1/audit/verify", nil)
	if code != 200 || chk["ok"] != true || chk["events"].(float64) < 24 {
		t.Fatalf("verify: %d %v", code, chk)
	}
	total := int64(chk["events"].(float64))

	hd, all := exportAudit(t, e, 0)
	if hd.Chain.Since != 0 || hd.Chain.PrevHash != "" || hd.Chain.Head.ID != total || int64(len(all)) != total {
		t.Fatalf("full export: %+v, %d events", hd.Chain, len(all))
	}
	if got := relink(t, "", all); got != hd.Chain.Head.Hash || got != chk["head"].(map[string]any)["hash"] {
		t.Fatalf("export head %s does not match chain %s", hd.Chain.Head.Hash, got)
	}

	cut := all[total/2-1]
	hd, tail := exportAudit(t, e, cut.ID)
	if hd.Chain.PrevHash != cut.Hash || len(tail) != len(all)-int(total/2) || tail[0].ID != cut.ID+1 {
		t.Fatalf("cursor export: %+v, first %d of %d", hd.Chain, tail[0].ID, len(tail))
	}
	if got := relink(t, hd.Chain.PrevHash, tail); got != hd.Chain.Head.Hash {
		t.Fatal("cursor export does not chain to head")
	}

	hd, none := exportAudit(t, e, total+100)
	if len(none) != 0 || hd.Chain.PrevHash != hd.Chain.Head.Hash || hd.Chain.Head.ID != total {
		t.Fatalf("export past head: %+v %d", hd.Chain, len(none))
	}

	raw, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`UPDATE audit SET agent_id = 'someone-else' WHERE id = 5`); err == nil {
		t.Fatal("append-only trigger missing")
	}
	if _, err := raw.Exec(`DROP TRIGGER audit_no_update; UPDATE audit SET agent_id = 'someone-else' WHERE id = 5`); err != nil {
		t.Fatal(err)
	}
	code, chk = e.REST("GET", "/api/v1/audit/verify", nil)
	if code != 200 || chk["ok"] != false || chk["broken_id"] != float64(5) || chk["events"] != float64(4) {
		t.Fatalf("tampered verify: %d %v", code, chk)
	}
}

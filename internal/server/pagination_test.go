package server_test

import (
	"context"
	"fmt"
	"net/url"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/servertest"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	httpSeedRows  = 5000
	httpHeavyRows = 500
	httpExtraRows = 100
	httpPageSize  = 100
)

type httpKind struct {
	name   string
	path   string
	key    string
	rows   int
	id     func(i int) string
	insert func(tx store.Tx, i int) error
}

func httpKinds() []httpKind {
	base := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	at := func(i int) time.Time { return base.Add(time.Duration(i) * time.Millisecond) }
	return []httpKind{
		{"tasks", "/api/v1/tasks", "tasks", httpSeedRows, func(i int) string { return fmt.Sprintf("T-%d", i) }, func(tx store.Tx, i int) error {
			return tx.InsertTask(&model.Task{ID: fmt.Sprintf("T-%d", i), BriefID: "B-1", PlanID: "P-1", Title: "t", Spec: "s", Repo: "o/r",
				Role: model.RoleWorker, Status: model.StatusReady, Priority: i % 5,
				AcceptanceCriteria: []string{}, DependsOn: []string{}, CreatedAt: at(i), UpdatedAt: at(i)})
		}},
		{"agents", "/api/v1/agents", "agents", httpSeedRows, func(i int) string { return fmt.Sprintf("ag-%06d", i) }, func(tx store.Tx, i int) error {
			return tx.InsertAgent(&model.Agent{ID: fmt.Sprintf("ag-%06d", i), Role: "worker", Kind: "other", Model: "m",
				Provider: "p", Status: "active", CreatedAt: at(i), TokenHash: fmt.Sprintf("h%d", i)})
		}},
		{"briefs", "/api/v1/briefs", "briefs", httpHeavyRows, func(i int) string { return fmt.Sprintf("B-%d", i) }, func(tx store.Tx, i int) error {
			return tx.InsertBrief(&model.Brief{ID: fmt.Sprintf("B-%d", i), Title: "t", Status: "open", PlanStatus: "none", CreatedAt: at(i)})
		}},
		{"plans", "/api/v1/plans", "plans", httpHeavyRows, func(i int) string { return fmt.Sprintf("P-%d", i) }, func(tx store.Tx, i int) error {
			return tx.InsertPlan(&model.Plan{ID: fmt.Sprintf("P-%d", i), BriefID: "B-1", Status: model.PlanProposed, ProposedBy: "rex", CreatedAt: at(i)})
		}},
		{"questions", "/api/v1/questions", "questions", httpSeedRows, func(i int) string { return fmt.Sprintf("Q-%d", i) }, func(tx store.Tx, i int) error {
			return tx.InsertQuestion(&model.Question{ID: fmt.Sprintf("Q-%d", i), AgentID: "a", Question: "q", Status: "open", CreatedAt: at(i / 3)})
		}},
		{"spawns", "/api/v1/spawns", "spawns", httpHeavyRows, func(i int) string { return fmt.Sprintf("S-%d", i) }, func(tx store.Tx, i int) error {
			return tx.InsertSpawn(&model.Spawn{ID: fmt.Sprintf("S-%d", i), BriefID: "B-1", AgentID: "a", Kind: "other", Model: "m",
				Provider: "p", Status: "requested", Spawner: "noop", RequestedBy: "rex", CreatedAt: at(i / 3)})
		}},
		{"messages", "/api/v1/threads/task/T-1/messages", "messages", httpSeedRows, func(i int) string { return fmt.Sprint(i) }, func(tx store.Tx, i int) error {
			return tx.InsertMessage(&model.Message{ThreadKind: "task", ThreadID: "T-1", AuthorID: "a", AuthorRole: "worker",
				Kind: "note", Body: "b", CreatedAt: at(i)})
		}},
		{"audit", "/api/v1/audit", "events", httpSeedRows, func(i int) string { return fmt.Sprint(i) }, func(tx store.Tx, i int) error {
			return tx.InsertAudit(&model.AuditEvent{TS: at(i), AgentID: "a", Role: "worker", Tool: "x"})
		}},
	}
}

func walkREST(e *servertest.Env, path, key string, limit int) []string {
	e.T.Helper()
	var ids []string
	cursor := ""
	for {
		q := url.Values{"limit": {fmt.Sprint(limit)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		sep := "?"
		code, out := e.REST("GET", path+sep+q.Encode(), nil)
		if code != 200 {
			e.T.Fatalf("GET %s: %d %v", path, code, out)
		}
		items, _ := out[key].([]any)
		if len(items) > limit {
			e.T.Fatalf("GET %s returned %d rows for limit %d", path, len(items), limit)
		}
		for _, it := range items {
			ids = append(ids, fmt.Sprint(it.(map[string]any)["id"]))
		}
		next, ok := out["next_cursor"]
		if !ok {
			e.T.Fatalf("GET %s: next_cursor missing from %v", path, out)
		}
		if next == nil {
			return ids
		}
		if len(items) == 0 {
			e.T.Fatalf("GET %s: empty page with next_cursor", path)
		}
		cursor = next.(string)
	}
}

func TestPaginationNoDuplicatesOrGapsWhileInserting(t *testing.T) {
	e := servertest.New(t)
	kinds := httpKinds()
	err := e.Srv.Store.Atomic(context.Background(), func(tx store.Tx) error {
		for _, k := range kinds {
			for i := 1; i <= k.rows; i++ {
				if err := k.insert(tx, i); err != nil {
					return fmt.Errorf("seed %s %d: %w", k.name, i, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errc := make(chan error, len(kinds))
	for _, k := range kinds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := k.rows + 1; i <= k.rows+httpExtraRows; i++ {
				if err := e.Srv.Store.Atomic(context.Background(), func(tx store.Tx) error { return k.insert(tx, i) }); err != nil {
					errc <- fmt.Errorf("%s insert: %w", k.name, err)
					return
				}
				runtime.Gosched()
			}
		}()
	}
	walked := map[string][]string{}
	for _, k := range kinds {
		walked[k.name] = walkREST(e, k.path, k.key, httpPageSize)
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}

	for _, k := range kinds {
		seen := walked[k.name]
		final := walkREST(e, k.path, k.key, 1000)
		if len(final) != k.rows+httpExtraRows {
			t.Fatalf("%s: final listing has %d rows", k.name, len(final))
		}
		got := map[string]bool{}
		for _, id := range seen {
			if got[id] {
				t.Fatalf("%s: %s returned twice", k.name, id)
			}
			got[id] = true
		}
		pos := 0
		for _, id := range seen {
			for pos < len(final) && final[pos] != id {
				pos++
			}
			if pos == len(final) {
				t.Fatalf("%s: %s out of order or missing from the final listing", k.name, id)
			}
		}
		for i := 1; i <= k.rows; i++ {
			if !got[k.id(i)] {
				t.Fatalf("%s: seeded row %s skipped", k.name, k.id(i))
			}
		}
	}
}

func TestPaginationRejectsBadCursors(t *testing.T) {
	e := servertest.New(t)
	for _, k := range httpKinds()[:2] {
		if err := e.Srv.Store.Atomic(context.Background(), func(tx store.Tx) error {
			for i := 1; i <= 3; i++ {
				if err := k.insert(tx, i); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, out := e.REST("GET", "/api/v1/tasks?limit=1", nil)
	taskCursor, _ := out["next_cursor"].(string)
	if taskCursor == "" {
		t.Fatalf("no next_cursor: %v", out)
	}
	for _, path := range []string{
		"/api/v1/tasks?cursor=not-a-cursor",
		"/api/v1/agents?cursor=" + taskCursor,
		"/api/v1/audit?cursor=" + taskCursor,
	} {
		code, body := e.REST("GET", path, nil)
		if code != 400 || body["error"].(map[string]any)["code"] != "bad_request" {
			t.Errorf("GET %s: %d %v", path, code, body)
		}
	}
}

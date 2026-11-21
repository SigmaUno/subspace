package storetest

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/testmode"
)

const (
	pageSize = 97
	pageAll  = 1 << 20
)

var (
	pageSeedRows  = testmode.Size(600, 5000)
	pageExtraRows = testmode.Size(60, 100)
)

type pagedKind struct {
	name   string
	insert func(tx store.Tx, i int) error
	page   func(tx store.Tx, after string, limit int) ([]string, error)
}

func idsOf[T any](rows []T, err error, id func(T) string) ([]string, error) {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, id(r))
	}
	return out, err
}

func numAfter(after string) int64 {
	n, _ := strconv.ParseInt(after, 10, 64)
	return n
}

func pagedKinds() []pagedKind {
	base := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	at := func(i int) time.Time { return base.Add(time.Duration(i) * time.Millisecond) }
	return []pagedKind{
		{"tasks",
			func(tx store.Tx, i int) error {
				x := taskMin(fmt.Sprintf("T-%d", i), "B-1")
				x.Priority = i % 5
				return tx.InsertTask(x)
			},
			func(tx store.Tx, after string, limit int) ([]string, error) {
				rows, err := tx.ListTasks(store.TaskFilter{AfterID: after, Limit: limit})
				return idsOf(rows, err, func(x *model.Task) string { return x.ID })
			}},
		{"agents",
			func(tx store.Tx, i int) error {
				return tx.InsertAgent(&model.Agent{ID: fmt.Sprintf("ag-%06d", i), Role: "worker", Kind: "other", Model: "m",
					Provider: "p", Status: "active", CreatedAt: at(i), TokenHash: fmt.Sprintf("h%d", i)})
			},
			func(tx store.Tx, after string, limit int) ([]string, error) {
				rows, err := tx.ListAgents(store.AgentFilter{AfterID: after, Limit: limit})
				return idsOf(rows, err, func(x *model.Agent) string { return x.ID })
			}},
		{"briefs",
			func(tx store.Tx, i int) error { return tx.InsertBrief(testBrief(fmt.Sprintf("B-%d", i), at(i))) },
			func(tx store.Tx, after string, limit int) ([]string, error) {
				rows, err := tx.ListBriefs(store.BriefFilter{AfterID: after, Limit: limit})
				return idsOf(rows, err, func(x *model.Brief) string { return x.ID })
			}},
		{"plans",
			func(tx store.Tx, i int) error {
				return tx.InsertPlan(&model.Plan{ID: fmt.Sprintf("P-%d", i), BriefID: "B-1", Status: model.PlanProposed, ProposedBy: "rex", CreatedAt: at(i)})
			},
			func(tx store.Tx, after string, limit int) ([]string, error) {
				rows, err := tx.ListPlans(store.PlanFilter{AfterID: after, Limit: limit})
				return idsOf(rows, err, func(x *model.Plan) string { return x.ID })
			}},
		{"questions",
			func(tx store.Tx, i int) error {
				return tx.InsertQuestion(&model.Question{ID: fmt.Sprintf("Q-%d", i), AgentID: "a", Question: "q", Status: "open", CreatedAt: at(i / 3)})
			},
			func(tx store.Tx, after string, limit int) ([]string, error) {
				rows, err := tx.ListQuestions(store.QuestionFilter{AfterID: after, Limit: limit})
				return idsOf(rows, err, func(x *model.Question) string { return x.ID })
			}},
		{"spawns",
			func(tx store.Tx, i int) error {
				return tx.InsertSpawn(&model.Spawn{ID: fmt.Sprintf("S-%d", i), BriefID: "B-1", AgentID: "a", Kind: "other", Model: "m",
					Provider: "p", Status: "requested", Spawner: "noop", RequestedBy: "rex", CreatedAt: at(i / 3)})
			},
			func(tx store.Tx, after string, limit int) ([]string, error) {
				rows, err := tx.ListSpawns(store.SpawnFilter{AfterID: after, Limit: limit})
				return idsOf(rows, err, func(x *model.Spawn) string { return x.ID })
			}},
		{"messages",
			func(tx store.Tx, i int) error {
				return tx.InsertMessage(&model.Message{ThreadKind: "task", ThreadID: "T-1", AuthorID: "a", AuthorRole: "worker",
					Kind: "note", Body: "b", CreatedAt: at(i)})
			},
			func(tx store.Tx, after string, limit int) ([]string, error) {
				rows, err := tx.ListMessages("task", "T-1", numAfter(after), limit)
				return idsOf(rows, err, func(x *model.Message) string { return strconv.FormatInt(x.ID, 10) })
			}},
		{"audit",
			func(tx store.Tx, i int) error {
				return tx.InsertAudit(&model.AuditEvent{TS: at(i), AgentID: "a", Role: "worker", Tool: "x"})
			},
			func(tx store.Tx, after string, limit int) ([]string, error) {
				rows, err := tx.ListAudit(store.AuditFilter{SinceID: numAfter(after), Limit: limit})
				return idsOf(rows, err, func(x *model.AuditEvent) string { return strconv.FormatInt(x.ID, 10) })
			}},
	}
}

func listPage(t *testing.T, s store.Store, k pagedKind, after string, limit int) []string {
	t.Helper()
	var ids []string
	read(t, s, func(tx store.Tx) (err error) { ids, err = k.page(tx, after, limit); return })
	return ids
}

func scenarioKeysetPagination(t *testing.T, s store.Store) {
	kinds := pagedKinds()
	write(t, s, func(tx store.Tx) error {
		for _, k := range kinds {
			for i := 1; i <= pageSeedRows; i++ {
				if err := k.insert(tx, i); err != nil {
					return fmt.Errorf("seed %s %d: %w", k.name, i, err)
				}
			}
		}
		return nil
	})
	seeded := map[string][]string{}
	for _, k := range kinds {
		seeded[k.name] = listPage(t, s, k, "", pageAll)
		if len(seeded[k.name]) != pageSeedRows {
			t.Fatalf("%s: seeded %d rows, listed %d", k.name, pageSeedRows, len(seeded[k.name]))
		}
	}

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		walked = map[string][]string{}
		errc   = make(chan error, 2*len(kinds))
	)
	for _, k := range kinds {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := pageSeedRows + 1; i <= pageSeedRows+pageExtraRows; i++ {
				if err := s.Atomic(context.Background(), func(tx store.Tx) error { return k.insert(tx, i) }); err != nil {
					errc <- fmt.Errorf("%s insert: %w", k.name, err)
					return
				}
				runtime.Gosched()
			}
		}()
		go func() {
			defer wg.Done()
			var seen []string
			after := ""
			for {
				var ids []string
				err := s.View(context.Background(), func(tx store.Tx) (err error) { ids, err = k.page(tx, after, pageSize); return })
				if err != nil {
					errc <- fmt.Errorf("%s page: %w", k.name, err)
					return
				}
				seen = append(seen, ids...)
				if len(ids) < pageSize {
					break
				}
				after = ids[len(ids)-1]
			}
			mu.Lock()
			walked[k.name] = seen
			mu.Unlock()
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}

	for _, k := range kinds {
		seen := walked[k.name]
		final := listPage(t, s, k, "", pageAll)
		dup := map[string]bool{}
		for _, id := range seen {
			if dup[id] {
				t.Fatalf("%s: %s returned twice", k.name, id)
			}
			dup[id] = true
		}
		pos := 0
		for _, id := range seen {
			for pos < len(final) && final[pos] != id {
				pos++
			}
			if pos == len(final) {
				t.Fatalf("%s: %s missing from final listing or out of order", k.name, id)
			}
		}
		for _, id := range seeded[k.name] {
			if !dup[id] {
				t.Fatalf("%s: seeded row %s skipped", k.name, id)
			}
		}
		t.Logf("%s: walked %d rows, %d inserted meanwhile", k.name, len(seen), len(seen)-pageSeedRows)
	}
}

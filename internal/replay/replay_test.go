package replay

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/dst"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/testmain"
	"github.com/SigmaUno/subspace/internal/testmode"
)

type capture struct {
	audit int64
	now   time.Time
	tasks []*model.Task
}

func head(s *dst.State) int64 {
	if len(s.Audit) == 0 {
		return 0
	}
	return s.Audit[len(s.Audit)-1].ID
}

func simulate(t *testing.T, f dst.Factory, seed int64, steps int) (*dst.State, []capture, map[string]string) {
	t.Helper()
	ctx := context.Background()
	w := dst.NewWorld(seed)
	clock := dst.NewClock(dst.Epoch)
	sys, err := f(t.TempDir(), clock, w.Lease)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sys.Close() })
	setup, err := sys.Setup(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	gen := dst.NewGen(seed, w, setup, true)
	st, err := sys.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	caps := []capture{{head(st), st.Now, st.Tasks}}
	for range steps {
		sys.Do(ctx, gen.Next(st))
		if st, err = sys.Snapshot(ctx); err != nil {
			t.Fatal(err)
		}
		caps = append(caps, capture{head(st), st.Now, st.Tasks})
	}
	roles := map[string]string{w.Leader: model.RoleLeader}
	for _, a := range w.Agents {
		roles[a.ID] = a.Role
	}
	return st, caps, roles
}

func TestMain(m *testing.M) { testmain.Run(m) }

func compare(t *testing.T, label string, snap Snapshot, live []*model.Task) {
	t.Helper()
	got := map[string]Task{}
	for _, col := range snap.Columns {
		for _, x := range col {
			got[x.ID] = x
		}
	}
	if len(got) != len(live) || snap.Total != len(live) {
		t.Fatalf("%s: replay has %d tasks (total %d), live board %d", label, len(got), snap.Total, len(live))
	}
	for _, l := range live {
		r, ok := got[l.ID]
		if !ok {
			t.Fatalf("%s: task %s missing from replay", label, l.ID)
		}
		if r.Status != l.Status || deref(r.Assignee) != deref(l.Assignee) || deref(r.Reviewer) != deref(l.Reviewer) ||
			r.ProgressPct != l.ProgressPct || r.CostUsed != l.CostUsed {
			t.Fatalf("%s: task %s replay %s/%q/%q/%d%%/%v, live %s/%q/%q/%d%%/%v", label, l.ID,
				r.Status, deref(r.Assignee), deref(r.Reviewer), r.ProgressPct, r.CostUsed,
				l.Status, deref(l.Assignee), deref(l.Reviewer), l.ProgressPct, l.CostUsed)
		}
	}
}

func TestReplayMatchesLiveBoard(t *testing.T) {
	seeds, steps := 10, 300
	if !testmode.Full() {
		seeds, steps = 3, 200
	}
	factories := map[string]dst.Factory{
		"board":      dst.NewBoardSystem,
		"mcp_faults": dst.NewMCPSystem(dst.MCPOptions{FaultSeed: 1, DropPct: 10, DupPct: 10}),
	}
	for name, f := range factories {
		for seed := int64(1); seed <= int64(seeds); seed++ {
			t.Run(fmt.Sprintf("%s/%d", name, seed), func(t *testing.T) {
				t.Parallel()
				final, caps, roles := simulate(t, f, seed, steps)
				briefID := final.Briefs[0].ID
				highs := make([]time.Time, len(caps))
				var high time.Time
				k := 0
				for i, c := range caps {
					for ; k < len(final.Audit) && final.Audit[k].ID <= c.audit; k++ {
						if ts := final.Audit[k].TS; ts.After(high) {
							high = ts
						}
					}
					highs[i] = high
				}
				for _, every := range []int{1, 7, DefaultEvery} {
					h := NewHistory(briefID, every)
					h.Update(final.Tasks, roles)
					h.Append(final.Audit)

					n, err := h.Resolve(strconv.FormatInt(head(final), 10))
					if err != nil {
						t.Fatal(err)
					}
					compare(t, "final", h.Snapshot(n), final.Tasks)
					compare(t, "latest", h.Snapshot(h.Len()), final.Tasks)

					for i, c := range caps {
						n, err := h.Resolve(strconv.FormatInt(c.audit, 10))
						if err != nil {
							t.Fatal(err)
						}
						compare(t, fmt.Sprintf("every=%d step %d audit %d", every, i, c.audit), h.Snapshot(n), c.tasks)
					}
					seen := map[time.Time]bool{}
					for _, c := range caps {
						if seen[c.now] {
							continue
						}
						seen[c.now] = true
						var want []*model.Task
						for j, x := range caps {
							if !highs[j].After(c.now) {
								want = x.tasks
							}
						}
						at := c.now.Format(time.RFC3339Nano)
						n, err := h.Resolve(at)
						if err != nil {
							t.Fatal(err)
						}
						compare(t, fmt.Sprintf("every=%d at %s", every, at), h.Snapshot(n), want)
					}

					checkTimelineAndDiff(t, h, caps, final)
				}
			})
		}
	}
}

func checkTimelineAndDiff(t *testing.T, h *History, caps []capture, final *dst.State) {
	t.Helper()
	tl := h.Timeline()
	if len(tl.Points) == 0 || len(tl.Points) > MaxPoints || len(tl.KeyEvents) > MaxPoints {
		t.Fatalf("timeline has %d points and %d key events", len(tl.Points), len(tl.KeyEvents))
	}
	last := tl.Points[len(tl.Points)-1]
	want := map[string]int{}
	for _, x := range final.Tasks {
		want[x.Status]++
	}
	if latest := h.Snapshot(h.Len()).AuditID; last.AuditID != latest || fmt.Sprint(last.Counts) != fmt.Sprint(want) {
		t.Fatalf("last timeline point %d %v, want %d %v", last.AuditID, last.Counts, latest, want)
	}

	mid := caps[len(caps)/2]
	from, _ := h.Resolve(strconv.FormatInt(mid.audit, 10))
	d := h.Diff(from, h.Len())
	status := map[string]string{}
	for _, x := range mid.tasks {
		status[x.ID] = x.Status
	}
	for _, tr := range d.Transitions {
		c, ok := tr.Changes["status"]
		if !ok {
			continue
		}
		if c.From != nil && c.From != status[tr.TaskID] {
			t.Fatalf("diff transition %d on %s starts at %v, task was %s", tr.AuditID, tr.TaskID, c.From, status[tr.TaskID])
		}
		status[tr.TaskID] = c.To.(string)
	}
	for _, x := range final.Tasks {
		if status[x.ID] != x.Status {
			t.Fatalf("diff from audit %d leaves %s at %s, live %s", mid.audit, x.ID, status[x.ID], x.Status)
		}
	}
}

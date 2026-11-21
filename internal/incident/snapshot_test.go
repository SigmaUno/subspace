package incident_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/SigmaUno/subspace/internal/incident"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/faultstore"
)

func seedBoard(t *testing.T, st store.Store) {
	t.Helper()
	err := st.Atomic(context.Background(), func(tx store.Tx) error {
		if err := tx.InsertBrief(&model.Brief{ID: "B-1", Title: "brief", Status: "active", CreatedAt: clock}); err != nil {
			return err
		}
		if err := tx.InsertAgent(&model.Agent{ID: "w1", Role: model.RoleWorker, Kind: "claude", Status: model.AgentActive, CreatedAt: clock}); err != nil {
			return err
		}
		for _, id := range []string{"T-1", "T-2"} {
			if err := tx.InsertTask(&model.Task{ID: id, BriefID: "B-1", Title: "task", Role: model.RoleWorker, Status: "todo", CreatedAt: clock, UpdatedAt: clock}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func snapshotResult(t *testing.T, svc *incident.Service) incident.Result {
	t.Helper()
	out, err := svc.Start(context.Background(), incident.Request{Reason: "x", Actions: []string{incident.Snapshot}})
	if err != nil {
		t.Fatal(err)
	}
	return result(t, out.Incident, incident.Snapshot)
}

func TestSnapshotWritesBoardState(t *testing.T) {
	st := openStore(t)
	seedBoard(t, st)
	dir := t.TempDir()
	svc := newService(t, st, &fakeControl{paused: true, why: "maintenance"}, nil, dir)

	r := snapshotResult(t, svc)
	path := filepath.Join(dir, "incidents", "INC-1.ndjson")
	if r.Status != incident.StatusOK || r.Detail != path {
		t.Fatalf("result %+v", r)
	}
	if names := dirNames(t, filepath.Join(dir, "incidents")); !slices.Equal(names, []string{"INC-1.ndjson"}) {
		t.Fatalf("incidents dir holds %v", names)
	}
	info, err := os.Stat(filepath.Join(dir, "incidents"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Fatalf("incidents dir mode %v", info.Mode().Perm())
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var kinds []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l struct {
			Kind string         `json:"kind"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, l.Kind)
		switch l.Kind {
		case "meta":
			head, _ := l.Data["audit_head"].(map[string]any)
			if l.Data["incident"] != "INC-1" || l.Data["paused"] != true || l.Data["pause_reason"] != "maintenance" || head["id"].(float64) < 1 {
				t.Fatalf("meta %v", l.Data)
			}
		case "brief", "agent", "task":
			if l.Data["id"] == nil {
				t.Fatalf("%s line without id: %v", l.Kind, l.Data)
			}
		}
	}
	if !slices.Equal(kinds, []string{"meta", "brief", "agent", "task", "task"}) {
		t.Fatalf("snapshot lines %v", kinds)
	}
}

func TestSnapshotFailuresAreReportedAndRetried(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string) string
		msg   string
	}{
		{"no data dir", func(*testing.T, string) string { return "" }, "no data directory configured for snapshots"},
		{"data dir is a file", func(t *testing.T, dir string) string {
			p := filepath.Join(dir, "file")
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}, "internal error"},
		{"read-only incidents dir", func(t *testing.T, dir string) string {
			if err := os.Mkdir(filepath.Join(dir, "incidents"), 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "incidents"), 0o700) })
			return dir
		}, "internal error"},
		{"target occupied", func(t *testing.T, dir string) string {
			if err := os.MkdirAll(filepath.Join(dir, "incidents", "INC-1.ndjson", "x"), 0o700); err != nil {
				t.Fatal(err)
			}
			return dir
		}, "internal error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if os.Geteuid() == 0 && tc.name == "read-only incidents dir" {
				t.Skip("root ignores directory permissions")
			}
			dir := t.TempDir()
			svc := newService(t, openStore(t), &fakeControl{}, nil, tc.setup(t, dir))
			r := snapshotResult(t, svc)
			if r.Status != incident.StatusFailed || r.Error != tc.msg || r.Detail != "" {
				t.Fatalf("result %+v", r)
			}
			if tmp, _ := filepath.Glob(filepath.Join(dir, "incidents", "*.tmp")); len(tmp) != 0 {
				t.Fatalf("temp files left: %v", tmp)
			}
		})
	}
}

func TestSnapshotReadFaultsLeaveNoFile(t *testing.T) {
	for i, method := range []string{"AuditHead", "ListBriefs", "ListAgents", "ListTasks"} {
		t.Run(method, func(t *testing.T) {
			st := openStore(t)
			seedBoard(t, st)
			fs := faultstore.New(st, 1, faultstore.Faults{Reads: true, FailAt: []int64{int64(6 + i)}})
			dir := t.TempDir()
			svc := newService(t, fs, &fakeControl{}, nil, dir)

			r := snapshotResult(t, svc)
			if ev := fs.Injected(); len(ev) != 1 || ev[0].Method != method {
				t.Fatalf("injected %+v", ev)
			}
			if r.Status != incident.StatusFailed || r.Error != "internal error" {
				t.Fatalf("result %+v", r)
			}
			if names := dirNames(t, filepath.Join(dir, "incidents")); len(names) != 0 {
				t.Fatalf("failed snapshot left %v", names)
			}

			fs.Set(faultstore.Faults{})
			if r := snapshotResult(t, svc); r.Status != incident.StatusOK {
				t.Fatalf("retry %+v", r)
			}
		})
	}
}

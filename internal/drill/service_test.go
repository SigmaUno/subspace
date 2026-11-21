package drill_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/alerts"
	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/backup"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/drill"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/faultstore"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

type backupsFunc func(context.Context) ([]backup.Info, error)

func (f backupsFunc) List(ctx context.Context) ([]backup.Info, error) { return f(ctx) }

type fetchFunc func(context.Context, io.Writer) (string, int64, error)

func (f fetchFunc) Fetch(ctx context.Context, w io.Writer) (string, int64, error) { return f(ctx, w) }

type plainStore struct{ store.Store }

type plainTx struct{ store.Tx }

func (s plainStore) Atomic(ctx context.Context, fn func(store.Tx) error) error {
	return s.Store.Atomic(ctx, func(tx store.Tx) error { return fn(plainTx{tx}) })
}

func (s plainStore) View(ctx context.Context, fn func(store.Tx) error) error {
	return s.Store.View(ctx, func(tx store.Tx) error { return fn(plainTx{tx}) })
}

type alertLog struct {
	mu     sync.Mutex
	events []published
}

type published struct {
	topics []string
	typ    string
	n      notify.Notification
}

func (a *alertLog) Publish(topics []string, typ string, data any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, published{topics, typ, data.(notify.Notification)})
}

func newDrill(t *testing.T, o drill.Options) *drill.Service {
	t.Helper()
	svc, err := drill.New(o)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func run(t *testing.T, svc *drill.Service) *drill.Result {
	t.Helper()
	res, err := svc.RunOnce(context.Background(), drill.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func execSQL(t *testing.T, st store.Store, q string, args ...any) {
	t.Helper()
	err := st.Atomic(context.Background(), func(tx store.Tx) error {
		_, err := tx.(interface {
			Exec(string, ...any) (sql.Result, error)
		}).Exec(q, args...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func fileBackup(t *testing.T, path string) backupsFunc {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return func(context.Context) ([]backup.Info, error) {
		return []backup.Info{{Name: filepath.Base(path), Path: path, Bytes: fi.Size()}}, nil
	}
}

func checkNames(res *drill.Result) []string {
	var out []string
	for _, c := range res.Checks {
		out = append(out, c.Name+"="+c.Status)
	}
	return out
}

func TestNewValidatesOptions(t *testing.T) {
	r := newRig(t, "")
	for name, o := range map[string]drill.Options{
		"no store":   {Backups: r.bk, Dir: r.dir},
		"no backups": {Store: r.live, Dir: r.dir},
		"no dir":     {Store: r.live, Backups: r.bk},
	} {
		if _, err := drill.New(o); err == nil {
			t.Fatalf("%s: New accepted %+v", name, o)
		}
	}
}

func TestHealthyDrillReportsEachCheck(t *testing.T) {
	r := newRig(t, "")
	r.briefs(3)
	info := r.backup()
	res := r.drill()
	checkStatus(t, res, drill.StatusOK)
	want := map[string]func(string) bool{
		"restore": func(d string) bool {
			return d == fmt.Sprintf("%s restored to a temporary database (%d bytes)", info.Name, info.Bytes)
		},
		"integrity_check": func(d string) bool { return d == "integrity_check ok" },
		"schema":          func(d string) bool { return d == "schema is current" },
		"audit_chain": func(d string) bool {
			return strings.HasPrefix(d, "audit chain intact, ") && !strings.HasSuffix(d, " 0 events")
		},
		"row_counts": func(d string) bool {
			return strings.HasPrefix(d, "backup/live rows briefs 3/3, plans 0/0, tasks 0/0, agents 0/0")
		},
		"boot": func(d string) bool {
			return strings.HasPrefix(d, "state served, 0 agents, 3 briefs, board B-") && strings.HasSuffix(d, " with 0 tasks")
		},
		"cleanup": func(d string) bool {
			return strings.HasPrefix(d, "temporary database drill-") && strings.HasSuffix(d, ".db deleted")
		},
	}
	for _, c := range res.Checks {
		if ok := want[c.Name]; ok == nil || !ok(c.Detail) {
			t.Fatalf("check %s detail %q", c.Name, c.Detail)
		}
	}
}

func TestRowCountTolerance(t *testing.T) {
	for _, tc := range []struct {
		tolerance float64
		live      int
		ok        bool
	}{
		{0, 100, true},
		{0, 101, false},
		{1, 101, false},
		{-0.5, 101, false},
		{0.9, 500, true},
		{0.9, 501, false},
	} {
		t.Run(fmt.Sprintf("%v/%d", tc.tolerance, tc.live), func(t *testing.T) {
			r := newRig(t, "")
			r.backup()
			err := r.live.Atomic(context.Background(), func(tx store.Tx) error {
				for i := range tc.live {
					if err := tx.InsertBrief(&model.Brief{ID: fmt.Sprintf("B-%d", i+1), Title: "b", Status: "active", CreatedAt: r.clk.Now()}); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			res := run(t, newDrill(t, drill.Options{Store: r.live, Backups: r.bk, Dir: r.dir, Tolerance: tc.tolerance, Now: r.clk.Now}))
			if !tc.ok {
				want := fmt.Sprintf("row_counts: briefs: backup has 0 of %d live rows", tc.live)
				if res.Status != drill.StatusFailed || res.Error != want {
					t.Fatalf("result %s %q, want %q", res.Status, res.Error, want)
				}
				return
			}
			checkStatus(t, res, drill.StatusOK)
			if boot := res.Checks[5]; boot.Name != "boot" || boot.Detail != "state served, 0 agents, 0 briefs" {
				t.Fatalf("boot %+v", boot)
			}
		})
	}
}

func TestRestoreFailures(t *testing.T) {
	r := newRig(t, "")
	r.briefs(1)
	info := r.backup()
	for _, tc := range []struct {
		name    string
		backups backupsFunc
		dir     func(t *testing.T) string
		err     string
	}{
		{"listing fails", func(context.Context) ([]backup.Info, error) { return nil, model.Conflict("backup dir unreadable") }, nil, "restore: backup dir unreadable"},
		{"file missing", func(context.Context) ([]backup.Info, error) {
			return []backup.Info{{Name: "gone.db", Path: filepath.Join(r.dir, "gone.db"), Bytes: 10}}, nil
		}, nil, "no such file or directory"},
		{"path is a directory", func(context.Context) ([]backup.Info, error) {
			return []backup.Info{{Name: "dir.db", Path: r.dir, Bytes: 10}}, nil
		}, nil, "is a directory"},
		{"size mismatch", func(context.Context) ([]backup.Info, error) {
			return []backup.Info{{Name: info.Name, Path: info.Path, Bytes: info.Bytes + 1}}, nil
		}, nil, fmt.Sprintf("restore: copied %d of %d bytes", info.Bytes, info.Bytes+1)},
		{"dir is a file", nil, func(t *testing.T) string {
			p := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}, "not a directory"},
		{"dir read-only", nil, func(t *testing.T) string {
			d := filepath.Join(t.TempDir(), "ro")
			if err := os.Mkdir(d, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(d, 0o700) })
			return d
		}, "permission denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if os.Geteuid() == 0 && tc.name == "dir read-only" {
				t.Skip("root ignores directory permissions")
			}
			dir := t.TempDir()
			if tc.dir != nil {
				dir = tc.dir(t)
			}
			bk := tc.backups
			if bk == nil {
				bk = r.bk.List
			}
			res := run(t, newDrill(t, drill.Options{Store: r.live, Backups: bk, Dir: dir, Now: r.clk.Now}))
			if res.Status != drill.StatusFailed || !strings.HasPrefix(res.Error, "restore: ") || !strings.Contains(res.Error, tc.err) {
				t.Fatalf("error %q, want restore error with %q", res.Error, tc.err)
			}
			if got := checkNames(res); !slices.Equal(got, []string{"restore=failed", "cleanup=ok"}) {
				t.Fatalf("checks %v", got)
			}
			if left, _ := filepath.Glob(filepath.Join(dir, "drill-*")); len(left) != 0 {
				t.Fatalf("left %v", left)
			}
		})
	}
}

func TestRestoreClearsStaleCopies(t *testing.T) {
	r := newRig(t, "")
	r.briefs(1)
	r.backup()
	dir := filepath.Join(t.TempDir(), "drills")
	svc := newDrill(t, drill.Options{Store: r.live, Backups: r.bk, Dir: dir, Now: r.clk.Now})
	checkStatus(t, run(t, svc), drill.StatusOK)
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o750 {
		t.Fatalf("drill dir mode %v", fi.Mode().Perm())
	}
	for _, name := range []string{"drill-1.db", "drill-1.db-wal", "keep.db"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	checkStatus(t, run(t, svc), drill.StatusOK)
	left, _ := os.ReadDir(dir)
	if len(left) != 1 || left[0].Name() != "keep.db" {
		t.Fatalf("dir holds %v", left)
	}
}

func tamperedBackup(t *testing.T, info backup.Info, stmts ...string) backupsFunc {
	t.Helper()
	path := filepath.Join(t.TempDir(), info.Name)
	copyFile(t, info.Path, path)
	bad, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range stmts {
		execSQL(t, bad, q)
	}
	if err := bad.Close(); err != nil {
		t.Fatal(err)
	}
	return fileBackup(t, path)
}

func TestTamperedBackupsFail(t *testing.T) {
	r := newRig(t, "")
	r.briefs(2)
	info := r.backup()
	for _, tc := range []struct {
		name  string
		stmts []string
		err   string
	}{
		{"forged audit", []string{`DROP TRIGGER audit_no_update`, `UPDATE audit SET note = 'forged' WHERE id = 1`}, "audit_chain: audit chain broken at event 1"},
		{"newer schema", []string{`UPDATE schema_version SET version = version + 1 WHERE id = 1`}, "schema: open with current code: "},
		{"dropped column", []string{`ALTER TABLE restore_drills DROP COLUMN error`}, "schema: 1 tables or columns still missing after migrations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := run(t, newDrill(t, drill.Options{Store: r.live, Backups: tamperedBackup(t, info, tc.stmts...), Dir: r.dir, Now: r.clk.Now}))
			if res.Status != drill.StatusFailed || !strings.HasPrefix(res.Error, tc.err) {
				t.Fatalf("result %s %q", res.Status, res.Error)
			}
			r.noLeftovers()
		})
	}
}

func TestCleanupFailureIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	r := newRig(t, "")
	r.briefs(1)
	info := r.backup()
	dir := filepath.Join(t.TempDir(), "drills")
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	remote := fetchFunc(func(_ context.Context, w io.Writer) (string, int64, error) {
		f, err := os.Open(info.Path)
		if err != nil {
			return "", 0, err
		}
		defer func() { _ = f.Close() }()
		n, err := io.Copy(w, f)
		if err == nil {
			err = os.Chmod(dir, 0o500)
		}
		return "remote.db.enc", n, err
	})
	svc := newDrill(t, drill.Options{Store: r.live, Backups: r.bk, Remote: remote, FromRemote: true, Dir: dir, Now: r.clk.Now})
	res := run(t, svc)
	last := res.Checks[len(res.Checks)-1]
	if res.Status != drill.StatusFailed || last.Name != "cleanup" || last.Status != drill.StatusFailed {
		t.Fatalf("checks %+v", res.Checks)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	checkStatus(t, run(t, newDrill(t, drill.Options{Store: r.live, Backups: r.bk, Dir: dir, Now: r.clk.Now})), drill.StatusOK)
	if left, _ := filepath.Glob(filepath.Join(dir, "drill-*")); len(left) != 0 {
		t.Fatalf("left %v", left)
	}
}

func TestLiveCountFailureFailsRowCounts(t *testing.T) {
	r := newRig(t, "")
	r.briefs(1)
	r.backup()
	fs := faultstore.New(r.live, 1, faultstore.Faults{Reads: true, FailAt: []int64{2}})
	res := run(t, newDrill(t, drill.Options{Store: fs, Backups: r.bk, Dir: r.dir, Now: r.clk.Now}))
	if ev := fs.Injected(); len(ev) != 1 || ev[0].Method != "Query" {
		t.Fatalf("injected %+v", ev)
	}
	if res.Status != drill.StatusFailed || res.Error != "row_counts: faultstore: injected fault: Query" {
		t.Fatalf("result %s %q", res.Status, res.Error)
	}
	r.noLeftovers()
}

func TestConcurrentRunIsRejected(t *testing.T) {
	r := newRig(t, "")
	entered, release := make(chan struct{}), make(chan struct{})
	bk := backupsFunc(func(context.Context) ([]backup.Info, error) {
		close(entered)
		<-release
		return nil, nil
	})
	svc := newDrill(t, drill.Options{Store: r.live, Backups: bk, Dir: r.dir, Now: r.clk.Now})
	done := make(chan *drill.Result)
	go func() {
		res, _ := svc.RunOnce(context.Background(), drill.TriggerManual)
		done <- res
	}()
	<-entered
	_, err := svc.RunOnce(context.Background(), drill.TriggerManual)
	if e := model.AsError(err); err == nil || e.Code != "conflict" {
		t.Fatalf("second run err = %v", err)
	}
	close(release)
	if res := <-done; res == nil || res.ID != "D-1" {
		t.Fatalf("first run %+v", res)
	}
}

func TestCancelledRunIsNotRecorded(t *testing.T) {
	r := newRig(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	bk := backupsFunc(func(ctx context.Context) ([]backup.Info, error) {
		cancel()
		return nil, ctx.Err()
	})
	pub := &alertLog{}
	svc := newDrill(t, drill.Options{Store: r.live, Backups: bk, Dir: r.dir, Pub: pub, Now: r.clk.Now})
	if _, err := svc.RunOnce(ctx, drill.TriggerManual); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if list, err := svc.List(context.Background(), 10); err != nil || len(list) != 0 || len(pub.events) != 0 {
		t.Fatalf("cancelled run recorded: %v %v %v", list, err, pub.events)
	}
}

func TestRecordFaultsFailTheRun(t *testing.T) {
	for op := int64(1); op <= 4; op++ {
		r := newRig(t, "")
		fs := faultstore.New(r.live, 1, faultstore.Faults{FailAt: []int64{op}})
		pub := &alertLog{}
		svc := newDrill(t, drill.Options{Store: fs, Backups: r.bk, Dir: r.dir, Pub: pub, Now: r.clk.Now})
		if _, err := svc.RunOnce(context.Background(), drill.TriggerManual); !errors.Is(err, faultstore.ErrInjected) {
			t.Fatalf("op %d: err = %v", op, err)
		}
		if list, err := svc.List(context.Background(), 10); err != nil || len(list) != 0 || len(pub.events) != 0 {
			t.Fatalf("op %d: list %v %v events %v", op, list, err, pub.events)
		}
	}
}

func TestStoreWithoutSQL(t *testing.T) {
	r := newRig(t, "")
	ctx := context.Background()
	if !drill.SupportsSQL(ctx, r.live) || drill.SupportsSQL(ctx, plainStore{r.live}) {
		t.Fatal("SupportsSQL misreports")
	}
	svc := newDrill(t, drill.Options{Store: plainStore{r.live}, Backups: r.bk, Dir: r.dir, Now: r.clk.Now})
	if _, err := svc.RunOnce(ctx, drill.TriggerManual); err == nil {
		t.Fatal("RunOnce succeeded without SQL")
	}
	if _, err := svc.Decisions(ctx); err == nil {
		t.Fatal("Decisions succeeded without SQL")
	}
}

func TestReadFaultsSurface(t *testing.T) {
	r := newRig(t, "")
	fs := faultstore.New(r.live, 1, faultstore.Faults{Reads: true, FailAt: []int64{1, 2}})
	svc := newDrill(t, drill.Options{Store: fs, Backups: r.bk, Dir: r.dir, Now: r.clk.Now})
	if _, err := svc.List(context.Background(), 10); !errors.Is(err, faultstore.ErrInjected) {
		t.Fatalf("List err = %v", err)
	}
	if _, err := svc.RunOnce(context.Background(), drill.TriggerManual); !errors.Is(err, faultstore.ErrInjected) {
		t.Fatalf("RunOnce err = %v", err)
	}
	if list, err := r.svc.List(context.Background(), 10); err != nil || len(list) != 0 {
		t.Fatalf("drill recorded without its predecessor: %v %v", list, err)
	}
}

func TestCorruptRowsSurfaceErrors(t *testing.T) {
	for name, row := range map[string][]any{
		"checks":     {"{", 1000},
		"started_at": {"[]", "yesterday"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, "")
			execSQL(t, r.live, `INSERT INTO restore_drills (id, status, trigger_kind, checks, started_at, finished_at) VALUES ('D-1', 'ok', 'manual', ?, ?, 0)`, row...)
			if _, err := r.svc.List(context.Background(), 10); err == nil {
				t.Fatal("List succeeded")
			}
		})
	}
}

func TestDrillRecordAndAudit(t *testing.T) {
	r := newRig(t, "")
	start := time.Date(2026, 11, 21, 3, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	calls := 0
	now := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return start.Add(time.Duration(calls) * time.Second)
	}
	svc := newDrill(t, drill.Options{Store: r.live, Backups: r.bk, Dir: r.dir, Now: now})
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{TokenID: "tok-1"})

	manual, err := svc.RunOnce(ctx, drill.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunOnce(ctx, drill.TriggerSchedule); err != nil {
		t.Fatal(err)
	}
	unsupported := newDrill(t, drill.Options{Store: r.live, Backups: r.bk, Unsupported: "not here", Now: now})
	if _, err := unsupported.RunOnce(ctx, drill.TriggerManual); err != nil {
		t.Fatal(err)
	}

	list, err := svc.List(context.Background(), 10)
	if err != nil || len(list) != 3 {
		t.Fatalf("list %v %v", list, err)
	}
	got := list[2]
	if got.ID != manual.ID || !got.StartedAt.Equal(manual.StartedAt) || !got.FinishedAt.Equal(manual.FinishedAt) || !got.FinishedAt.After(got.StartedAt) {
		t.Fatalf("stored %+v, ran %+v", got, manual)
	}

	var ev []*model.AuditEvent
	if err := r.live.View(context.Background(), func(tx store.Tx) (err error) {
		ev, err = tx.ListAudit(store.AuditFilter{Tool: "restore_drill", Limit: 10})
		return
	}); err != nil {
		t.Fatal(err)
	}
	notes := map[string]string{}
	afters := map[string]map[string]any{}
	for _, e := range ev {
		id, _ := e.After["id"].(string)
		notes[id], afters[id] = *e.Note, e.After
	}
	if notes["D-1"] != "token=tok-1; manual drill" || notes["D-2"] != "schedule drill" || notes["D-3"] != "token=tok-1; manual drill" {
		t.Fatalf("notes %v", notes)
	}
	if a := afters["D-1"]; a["failed_check"] != "restore" || a["error"] != "restore: no backup available" || a["status"] != drill.StatusFailed {
		t.Fatalf("failed drill audit %v", a)
	}
	if _, ok := afters["D-3"]["failed_check"]; ok || afters["D-3"]["status"] != drill.StatusUnsupported {
		t.Fatalf("unsupported drill audit %v", afters["D-3"])
	}
}

func TestAlertRaisedAndResolved(t *testing.T) {
	r := newRig(t, "")
	pub := &alertLog{}
	svc := newDrill(t, drill.Options{Store: r.live, Backups: r.bk, Dir: r.dir, Pub: pub, Now: r.clk.Now})
	if ds, err := svc.Decisions(context.Background()); err != nil || len(ds) != 0 {
		t.Fatalf("decisions before any drill: %v %v", ds, err)
	}

	failed := run(t, svc)
	r.briefs(1)
	r.backup()
	passed := run(t, svc)
	checkStatus(t, passed, drill.StatusOK)
	run(t, svc)

	if len(pub.events) != 2 {
		t.Fatalf("events %+v", pub.events)
	}
	raised, resolved := pub.events[0], pub.events[1]
	base := notify.Notification{ID: "alert:restore_drill", Kind: decisions.KindAlert, Title: "Restore drill failed", Priority: 5}
	if !slices.Equal(raised.topics, []string{alerts.TopicAlerts}) || raised.typ != alerts.EventRaised ||
		raised.n.ID != base.ID || raised.n.Kind != base.Kind || raised.n.Title != base.Title || raised.n.Priority != base.Priority ||
		raised.n.Text != "restore: no backup available" || !raised.n.TS.Equal(failed.FinishedAt) {
		t.Fatalf("raised %+v", raised)
	}
	if resolved.typ != alerts.EventResolved || resolved.n.ID != base.ID || resolved.n.Priority != base.Priority || resolved.n.Text != "Restore drill "+passed.ID+" passed" {
		t.Fatalf("resolved %+v", resolved)
	}
}

func TestSchedulerInterval(t *testing.T) {
	r := newRig(t, "")
	for _, tc := range []struct {
		every     time.Duration
		scheduled bool
	}{
		{0, false},
		{-time.Hour, false},
		{time.Nanosecond, true},
	} {
		var got []time.Duration
		svc := newDrill(t, drill.Options{Store: r.live, Backups: r.bk, Dir: r.dir, Every: tc.every, Now: r.clk.Now,
			NewTicker: func(d time.Duration) backup.Ticker { got = append(got, d); return make(tick) }})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		svc.Run(ctx)
		if tc.scheduled != (len(got) == 1) || tc.scheduled && got[0] != tc.every {
			t.Fatalf("every %v: tickers %v", tc.every, got)
		}
	}
}

package drill_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/backup"
	"github.com/SigmaUno/subspace/internal/backup/offsite"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/drill"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type event struct{ topics, typ string }

type recorder struct {
	mu     sync.Mutex
	events []event
}

func (r *recorder) Publish(topics []string, typ string, _ any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event{topics[0], typ})
}

func (r *recorder) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		out = append(out, e.topics+"/"+e.typ)
	}
	return out
}

type tick chan time.Time

func (t tick) C() <-chan time.Time { return t }
func (t tick) Stop()               {}

type rig struct {
	t     *testing.T
	clk   *clock
	dir   string
	live  *sqlite.Store
	board *board.Board
	bk    *backup.Scheduler
	pub   *recorder
	svc   *drill.Service
	tk    tick
}

func newRig(t *testing.T, livePath string) *rig {
	t.Helper()
	dir := t.TempDir()
	if livePath == "" {
		livePath = filepath.Join(dir, "live.db")
	}
	live, err := sqlite.Open(livePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	clk := &clock{t: time.Date(2026, 11, 21, 3, 0, 0, 0, time.UTC)}
	b, err := board.New(context.Background(), live, board.Options{Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	bk, err := backup.New(backup.Config{
		Dir: filepath.Join(dir, "backups"), Run: live.Backup, Verify: sqlite.IntegrityCheck, Now: clk.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, clk: clk, dir: dir, live: live, board: b, bk: bk, pub: &recorder{}, tk: make(tick)}
	svc, err := drill.New(drill.Options{
		Store: live, Backups: bk, Dir: dir, Every: time.Hour, Pub: r.pub, Now: clk.Now,
		NewTicker: func(time.Duration) backup.Ticker { return r.tk },
	})
	if err != nil {
		t.Fatal(err)
	}
	r.svc = svc
	return r
}

func (r *rig) briefs(n int) {
	r.t.Helper()
	for i := range n {
		if _, err := r.board.CreateBrief(context.Background(), board.BriefInput{Title: fmt.Sprintf("brief %d", i), Body: "body"}); err != nil {
			r.t.Fatal(err)
		}
	}
}

func (r *rig) backup() backup.Info {
	r.t.Helper()
	r.clk.Advance(time.Minute)
	info, err := r.bk.RunOnce(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	return info
}

func (r *rig) drill() *drill.Result {
	r.t.Helper()
	res, err := r.svc.RunOnce(context.Background(), drill.TriggerManual)
	if err != nil {
		r.t.Fatal(err)
	}
	return res
}

func (r *rig) noLeftovers() {
	r.t.Helper()
	left, _ := filepath.Glob(filepath.Join(r.dir, "drill-*"))
	if len(left) > 0 {
		r.t.Fatalf("temporary copies left behind: %v", left)
	}
}

func checkStatus(t *testing.T, res *drill.Result, want string) {
	t.Helper()
	if res.Status != want {
		t.Fatalf("status = %s (%s), want %s; checks %+v", res.Status, res.Error, want, res.Checks)
	}
}

func failedCheck(res *drill.Result) string {
	for _, c := range res.Checks {
		if c.Status == drill.StatusFailed {
			return c.Name
		}
	}
	return ""
}

func TestHealthyBackupPasses(t *testing.T) {
	r := newRig(t, "")
	r.briefs(3)
	info := r.backup()
	res := r.drill()
	checkStatus(t, res, drill.StatusOK)
	want := []string{"restore", "integrity_check", "schema", "audit_chain", "row_counts", "boot", "cleanup"}
	var got []string
	for _, c := range res.Checks {
		got = append(got, c.Name)
		if c.Status != drill.StatusOK {
			t.Fatalf("check %s: %s", c.Name, c.Detail)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("checks = %v, want %v", got, want)
	}
	if res.Backup != info.Name || res.BackupBytes != info.Bytes || res.ID != "D-1" || res.Trigger != drill.TriggerManual {
		t.Fatalf("result = %+v", res)
	}
	r.noLeftovers()
	if ev := r.pub.types(); len(ev) != 0 {
		t.Fatalf("passing drill raised %v", ev)
	}
	list, err := r.svc.List(context.Background(), 10)
	if err != nil || len(list) != 1 || list[0].ID != "D-1" || len(list[0].Checks) != len(want) {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

func TestNoBackupFails(t *testing.T) {
	r := newRig(t, "")
	res := r.drill()
	checkStatus(t, res, drill.StatusFailed)
	if failedCheck(res) != "restore" || res.Error != "restore: no backup available" {
		t.Fatalf("result = %+v", res)
	}
}

func TestCorruptedBackupRaisesAlertAndDecisionUntilFixed(t *testing.T) {
	r := newRig(t, "")
	r.briefs(5)
	bad := r.backup()
	data, err := os.ReadFile(bad.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad.Path, data[:len(data)/2], 0o600); err != nil {
		t.Fatal(err)
	}

	res := r.drill()
	checkStatus(t, res, drill.StatusFailed)
	if failedCheck(res) != "integrity_check" || res.Backup != bad.Name {
		t.Fatalf("result = %+v", res)
	}
	r.noLeftovers()
	if ev := r.pub.types(); !slices.Equal(ev, []string{"alerts/alert.raised"}) {
		t.Fatalf("events = %v", ev)
	}
	ds, err := r.svc.Decisions(context.Background())
	if err != nil || len(ds) != 1 || ds[0].Kind != decisions.KindAlert || ds[0].ID != "drill:"+res.ID {
		t.Fatalf("decisions = %+v, %v", ds, err)
	}

	r.backup()
	res = r.drill()
	checkStatus(t, res, drill.StatusOK)
	if ev := r.pub.types(); !slices.Equal(ev, []string{"alerts/alert.raised", "alerts/alert.resolved"}) {
		t.Fatalf("events = %v", ev)
	}
	if ds, err := r.svc.Decisions(context.Background()); err != nil || len(ds) != 0 {
		t.Fatalf("decisions after fix = %+v, %v", ds, err)
	}
}

func TestGarbageBackupFails(t *testing.T) {
	r := newRig(t, "")
	r.briefs(1)
	info := r.backup()
	if err := os.WriteFile(info.Path, []byte("definitely not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := r.drill()
	checkStatus(t, res, drill.StatusFailed)
	if c := failedCheck(res); c != "integrity_check" && c != "restore" {
		t.Fatalf("failed check = %s", c)
	}
	r.noLeftovers()
}

func TestTruncatedBackupDetectedByRowCounts(t *testing.T) {
	r := newRig(t, "")
	r.backup()
	r.briefs(120)
	res := r.drill()
	checkStatus(t, res, drill.StatusFailed)
	if failedCheck(res) != "row_counts" {
		t.Fatalf("result = %+v", res)
	}
	r.noLeftovers()
}

func TestNewerLiveRowsAreTolerated(t *testing.T) {
	r := newRig(t, "")
	r.briefs(60)
	r.backup()
	r.briefs(40)
	checkStatus(t, r.drill(), drill.StatusOK)
}

func TestBackupWithMoreRowsThanLiveFails(t *testing.T) {
	r := newRig(t, "")
	r.briefs(10)
	r.backup()
	empty, err := sqlite.Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = empty.Close() }()
	svc, err := drill.New(drill.Options{Store: empty, Backups: r.bk, Dir: r.dir, Now: r.clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.RunOnce(context.Background(), drill.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	checkStatus(t, res, drill.StatusFailed)
	if failedCheck(res) != "row_counts" {
		t.Fatalf("result = %+v", res)
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOldSchemaBackupsRestoreThroughMigrations(t *testing.T) {
	fixtures, err := filepath.Glob("../store/sqlite/testdata/fixtures/*.db")
	if err != nil || len(fixtures) < 6 {
		t.Fatalf("fixtures = %v, %v", fixtures, err)
	}
	for _, fx := range fixtures {
		t.Run(filepath.Base(fx), func(t *testing.T) {
			livePath := filepath.Join(t.TempDir(), "live.db")
			copyFile(t, fx, livePath)
			r := newRig(t, livePath)
			copyFile(t, fx, filepath.Join(r.dir, "backups", "subspace-20260101T000000Z.db"))
			res := r.drill()
			checkStatus(t, res, drill.StatusOK)
			if res.Backup != "subspace-20260101T000000Z.db" {
				t.Fatalf("backup = %s", res.Backup)
			}
			if d := res.Checks[2]; d.Name != "schema" || !strings.HasPrefix(d.Detail, "migrations added") {
				t.Fatalf("schema check = %+v", d)
			}
			r.noLeftovers()
		})
	}
}

func TestUnsupportedBackendIsReportedWithoutAlert(t *testing.T) {
	r := newRig(t, "")
	svc, err := drill.New(drill.Options{
		Store: r.live, Backups: r.bk, Unsupported: "postgres backups are not pg_dump files", Pub: r.pub, Now: r.clk.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.RunOnce(context.Background(), drill.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	checkStatus(t, res, drill.StatusUnsupported)
	if res.Error != "postgres backups are not pg_dump files" || len(res.Checks) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if ev := r.pub.types(); len(ev) != 0 {
		t.Fatalf("events = %v", ev)
	}
	if ds, _ := svc.Decisions(context.Background()); len(ds) != 0 {
		t.Fatalf("decisions = %+v", ds)
	}
}

func TestScheduleRunsOnTicks(t *testing.T) {
	r := newRig(t, "")
	r.briefs(2)
	r.backup()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.svc.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	for i := range 2 {
		r.tk <- r.clk.Now()
		waitFor(t, func() bool {
			list, _ := r.svc.List(context.Background(), 10)
			return len(list) == i+1
		})
	}
	list, _ := r.svc.List(context.Background(), 10)
	if list[0].ID != "D-2" || list[0].Trigger != drill.TriggerSchedule || list[0].Status != drill.StatusOK {
		t.Fatalf("list = %+v", list)
	}
	if !list[0].StartedAt.Equal(r.clk.Now()) {
		t.Fatalf("started %s, clock %s", list[0].StartedAt, r.clk.Now())
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *rig) offsite(fromRemote bool) (*drill.Service, string) {
	r.t.Helper()
	dest := r.t.TempDir()
	key := filepath.Join(r.t.TempDir(), "backup.key")
	if err := os.WriteFile(key, []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
		r.t.Fatal(err)
	}
	remote, err := offsite.Open(offsite.Config{
		Kind: offsite.KindFS, Path: dest, KeyFile: key, Retention: offsite.DefaultPolicy, Interval: time.Hour,
	}, offsite.Deps{Now: r.clk.Now})
	if err != nil {
		r.t.Fatal(err)
	}
	up, err := offsite.NewService(offsite.Options{
		Remote: remote, Store: r.live, Backups: r.bk, StageDir: filepath.Join(r.dir, "stage"), Now: r.clk.Now,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := up.Sync(context.Background()); err != nil {
		r.t.Fatal(err)
	}
	svc, err := drill.New(drill.Options{
		Store: r.live, Backups: r.bk, Remote: remote, FromRemote: fromRemote, Dir: r.dir, Every: time.Hour, Pub: r.pub, Now: r.clk.Now,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return svc, dest
}

func TestDrillPullsFromRemote(t *testing.T) {
	r := newRig(t, "")
	r.briefs(4)
	info := r.backup()
	svc, dest := r.offsite(true)
	if err := os.WriteFile(info.Path, []byte("local copy is garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := svc.RunOnce(context.Background(), drill.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	checkStatus(t, res, drill.StatusOK)
	if !strings.HasSuffix(res.Backup, ".db.enc") {
		t.Fatalf("drill used %q", res.Backup)
	}
	if !strings.Contains(res.Checks[0].Detail, "remote") {
		t.Fatalf("restore detail %q", res.Checks[0].Detail)
	}
	r.noLeftovers()

	objs, _ := filepath.Glob(filepath.Join(dest, "*.db.enc"))
	raw, err := os.ReadFile(objs[0])
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 1
	if err := os.WriteFile(objs[0], raw, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err = svc.RunOnce(context.Background(), drill.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	checkStatus(t, res, drill.StatusFailed)
	if failedCheck(res) != "restore" || !strings.Contains(res.Error, "authentication") {
		t.Fatalf("tampered remote backup: %+v", res)
	}
	r.noLeftovers()
}

func TestDrillFallsBackToRemoteWithoutLocalBackups(t *testing.T) {
	r := newRig(t, "")
	r.briefs(2)
	info := r.backup()
	svc, _ := r.offsite(false)
	if err := os.Remove(info.Path); err != nil {
		t.Fatal(err)
	}
	res, err := svc.RunOnce(context.Background(), drill.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	checkStatus(t, res, drill.StatusOK)
	if !strings.HasSuffix(res.Backup, ".db.enc") {
		t.Fatalf("drill used %q", res.Backup)
	}
}

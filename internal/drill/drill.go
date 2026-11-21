package drill

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/alerts"
	"github.com/SigmaUno/subspace/internal/backup"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

const (
	StatusOK          = "ok"
	StatusFailed      = "failed"
	StatusUnsupported = "unsupported"

	TriggerSchedule = "schedule"
	TriggerManual   = "manual"

	DefaultEvery     = 24 * time.Hour
	DefaultTolerance = 0.5

	idPrefix     = "D"
	rowSlack     = 50
	alertKey     = "restore_drill"
	alertPrio    = 5
	maxListLimit = 1000
)

var countedTables = []string{"briefs", "plans", "tasks", "agents", "messages", "questions", "spawns", "audit"}

type Backups interface {
	List(ctx context.Context) ([]backup.Info, error)
}

type RemoteSource interface {
	Fetch(ctx context.Context, w io.Writer) (name string, bytes int64, err error)
}

type Options struct {
	Store       store.Store
	Backups     Backups
	Remote      RemoteSource
	FromRemote  bool
	Dir         string
	Every       time.Duration
	Tolerance   float64
	Unsupported string
	Pub         events.Publisher
	Now         func() time.Time
	NewTicker   func(time.Duration) backup.Ticker
	Log         *slog.Logger
}

type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type Result struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	Trigger     string    `json:"trigger"`
	Backup      string    `json:"backup"`
	BackupBytes int64     `json:"backup_bytes"`
	Error       string    `json:"error"`
	Checks      []Check   `json:"checks"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
}

type Service struct {
	o   Options
	run sync.Mutex
}

func New(o Options) (*Service, error) {
	if o.Store == nil || o.Backups == nil {
		return nil, errors.New("drill: Store and Backups are required")
	}
	if o.Dir == "" && o.Unsupported == "" {
		return nil, errors.New("drill: Dir is required")
	}
	if o.Tolerance <= 0 || o.Tolerance >= 1 {
		o.Tolerance = DefaultTolerance
	}
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	if o.NewTicker == nil {
		o.NewTicker = backup.NewTicker
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	o.Log = o.Log.With("component", "drill")
	return &Service{o: o}, nil
}

func (s *Service) Run(ctx context.Context) {
	if s.o.Every <= 0 {
		s.o.Log.Info("scheduled restore drills disabled")
		return
	}
	tk := s.o.NewTicker(s.o.Every)
	defer tk.Stop()
	s.o.Log.Info("restore drill scheduler started", "every", s.o.Every)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C():
			if _, err := s.RunOnce(ctx, TriggerSchedule); err != nil && ctx.Err() == nil {
				s.o.Log.Error("restore drill could not run", "err", err)
			}
		}
	}
}

func (s *Service) RunOnce(ctx context.Context, trigger string) (*Result, error) {
	if !s.run.TryLock() {
		return nil, model.Conflict("a restore drill is already running")
	}
	defer s.run.Unlock()
	prev, err := s.latest(ctx)
	if err != nil {
		return nil, err
	}
	r := &Result{Trigger: trigger, Checks: []Check{}, StartedAt: s.o.Now().UTC()}
	if s.o.Unsupported != "" {
		r.Status, r.Error = StatusUnsupported, s.o.Unsupported
	} else {
		s.execute(ctx, r)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	r.FinishedAt = s.o.Now().UTC()
	if err := s.record(ctx, r); err != nil {
		return nil, err
	}
	s.notify(r, prev)
	return r, nil
}

func (s *Service) execute(ctx context.Context, r *Result) {
	w := &workspace{s: s, r: r}
	for _, st := range []struct {
		name string
		fn   func(context.Context) (string, error)
	}{
		{"restore", w.restore},
		{"integrity_check", w.integrity},
		{"schema", w.migrate},
		{"audit_chain", w.auditChain},
		{"row_counts", w.rowCounts},
		{"boot", w.boot},
	} {
		if !r.step(ctx, st.name, st.fn) {
			break
		}
	}
	r.step(ctx, "cleanup", func(context.Context) (string, error) { return w.cleanup() })
	if r.Status == "" {
		r.Status = StatusOK
	}
}

func (r *Result) step(ctx context.Context, name string, fn func(context.Context) (string, error)) bool {
	detail, err := fn(ctx)
	if err != nil {
		r.Checks = append(r.Checks, Check{Name: name, Status: StatusFailed, Detail: err.Error()})
		if r.Status != StatusFailed {
			r.Status, r.Error = StatusFailed, name+": "+err.Error()
		}
		return false
	}
	r.Checks = append(r.Checks, Check{Name: name, Status: StatusOK, Detail: detail})
	return true
}

func (r *Result) failedCheck() string {
	for _, c := range r.Checks {
		if c.Status == StatusFailed {
			return c.Name
		}
	}
	return ""
}

type workspace struct {
	s    *Service
	r    *Result
	path string
	st   store.Store
}

func (w *workspace) restore(ctx context.Context) (string, error) {
	list, err := w.s.o.Backups.List(ctx)
	if err != nil {
		return "", err
	}
	remote := w.s.o.Remote != nil && (w.s.o.FromRemote || len(list) == 0)
	if len(list) == 0 && !remote {
		return "", errors.New("no backup available")
	}
	if err := os.MkdirAll(w.s.o.Dir, 0o750); err != nil {
		return "", err
	}
	stale, _ := filepath.Glob(filepath.Join(w.s.o.Dir, "drill-*.db*"))
	for _, p := range stale {
		_ = os.Remove(p)
	}
	dst, err := os.CreateTemp(w.s.o.Dir, "drill-*.db")
	if err != nil {
		return "", err
	}
	w.path = dst.Name()
	var detail string
	if remote {
		detail, err = w.pull(ctx, dst)
	} else {
		detail, err = w.copyLocal(list[0], dst)
	}
	if err == nil {
		err = dst.Sync()
	}
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	return detail, nil
}

func (w *workspace) pull(ctx context.Context, dst io.Writer) (string, error) {
	name, n, err := w.s.o.Remote.Fetch(ctx, dst)
	w.r.Backup, w.r.BackupBytes = name, n
	if err != nil {
		return "", fmt.Errorf("remote backup: %w", err)
	}
	return fmt.Sprintf("%s downloaded from the remote, decrypted and restored to a temporary database (%d bytes)", name, n), nil
}

func (w *workspace) copyLocal(newest backup.Info, dst io.Writer) (string, error) {
	w.r.Backup, w.r.BackupBytes = newest.Name, newest.Bytes
	src, err := os.Open(newest.Path)
	if err != nil {
		return "", err
	}
	defer func() { _ = src.Close() }()
	n, err := io.Copy(dst, src)
	if err != nil {
		return "", err
	}
	if n != newest.Bytes {
		return "", fmt.Errorf("copied %d of %d bytes", n, newest.Bytes)
	}
	return fmt.Sprintf("%s restored to a temporary database (%d bytes)", newest.Name, n), nil
}

func (w *workspace) integrity(ctx context.Context) (string, error) {
	if err := sqlite.IntegrityCheck(ctx, w.path); err != nil {
		return "", err
	}
	return "integrity_check ok", nil
}

func (w *workspace) migrate(ctx context.Context) (string, error) {
	want, err := sqlite.ExpectedSchema(ctx)
	if err != nil {
		return "", err
	}
	before, err := missingSchema(ctx, w.path, want)
	if err != nil {
		return "", err
	}
	st, err := sqlite.Open(w.path)
	if err != nil {
		return "", fmt.Errorf("open with current code: %w", err)
	}
	w.st = st
	after, err := missingSchema(ctx, w.path, want)
	if err != nil {
		return "", err
	}
	if after > 0 {
		return "", fmt.Errorf("%d tables or columns still missing after migrations", after)
	}
	if before == 0 {
		return "schema is current", nil
	}
	return fmt.Sprintf("migrations added %d tables or columns", before), nil
}

func missingSchema(ctx context.Context, path string, want map[string][]string) (int, error) {
	db, err := sqlite.OpenReadOnly(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }()
	got, err := sqlite.ReadSchema(ctx, db)
	if err != nil {
		return 0, err
	}
	missing := 0
	for table, cols := range want {
		have, ok := got[table]
		if !ok {
			missing += len(cols)
			continue
		}
		for _, c := range cols {
			if !slices.Contains(have, c) {
				missing++
			}
		}
	}
	return missing, nil
}

func (w *workspace) auditChain(ctx context.Context) (string, error) {
	var check model.AuditCheck
	err := w.st.View(ctx, func(tx store.Tx) (err error) {
		check, err = tx.VerifyAudit()
		return
	})
	if err != nil {
		return "", err
	}
	if !check.OK {
		return "", fmt.Errorf("audit chain broken at event %d after %d verified events", check.BrokenID, check.Events)
	}
	return fmt.Sprintf("audit chain intact, %d events", check.Events), nil
}

func (w *workspace) rowCounts(ctx context.Context) (string, error) {
	var parts, bad []string
	for _, table := range countedTables {
		copied, err := countRows(ctx, w.st, table)
		if err != nil {
			return "", err
		}
		live, err := countRows(ctx, w.s.o.Store, table)
		if err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("%s %d/%d", table, copied, live))
		floor := float64(live)*(1-w.s.o.Tolerance) - rowSlack
		switch {
		case copied > live:
			bad = append(bad, fmt.Sprintf("%s: backup has %d rows, live only %d", table, copied, live))
		case float64(copied) < floor:
			bad = append(bad, fmt.Sprintf("%s: backup has %d of %d live rows", table, copied, live))
		}
	}
	if len(bad) > 0 {
		return "", errors.New(strings.Join(bad, "; "))
	}
	return "backup/live rows " + strings.Join(parts, ", "), nil
}

func countRows(ctx context.Context, st store.Store, table string) (int64, error) {
	var n int64
	err := st.View(ctx, func(tx store.Tx) error {
		q, err := store.SQLOf(tx)
		if err != nil {
			return err
		}
		rows, err := q.Query(`SELECT count(*) FROM ` + table)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		if rows.Next() {
			if err := rows.Scan(&n); err != nil {
				return err
			}
		}
		return rows.Err()
	})
	return n, err
}

func (w *workspace) boot(ctx context.Context) (string, error) {
	b, err := board.New(ctx, w.st, board.Options{Now: w.s.o.Now, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		return "", err
	}
	state, err := b.State(ctx)
	if err != nil {
		return "", fmt.Errorf("state: %w", err)
	}
	briefs, err := b.ListBriefs(ctx, "")
	if err != nil {
		return "", fmt.Errorf("briefs: %w", err)
	}
	detail := fmt.Sprintf("state served, %d agents, %d briefs", state.Counts["agents"], len(briefs))
	if len(briefs) == 0 {
		return detail, nil
	}
	view, err := b.Board(ctx, briefs[0].ID)
	if err != nil {
		return "", fmt.Errorf("board %s: %w", briefs[0].ID, err)
	}
	tasks := 0
	for _, col := range view.Columns {
		tasks += len(col)
	}
	return fmt.Sprintf("%s, board %s with %d tasks", detail, briefs[0].ID, tasks), nil
}

func (w *workspace) cleanup() (string, error) {
	var errs []error
	if w.st != nil {
		errs = append(errs, w.st.Close())
		w.st = nil
	}
	if w.path == "" {
		return "nothing to remove", errors.Join(errs...)
	}
	for _, p := range []string{w.path, w.path + "-wal", w.path + "-shm", w.path + "-journal"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return "", err
	}
	return "temporary database " + filepath.Base(w.path) + " deleted", nil
}

func (s *Service) notify(r, prev *Result) {
	if s.o.Pub == nil {
		return
	}
	n := notify.Notification{
		ID: "alert:" + alertKey, Kind: decisions.KindAlert, Title: "Restore drill failed", Text: r.Error,
		Priority: alertPrio, TS: r.FinishedAt,
	}
	switch {
	case r.Status == StatusFailed:
		s.o.Pub.Publish([]string{alerts.TopicAlerts}, alerts.EventRaised, n)
	case r.Status == StatusOK && prev != nil && prev.Status == StatusFailed:
		n.Text = "Restore drill " + r.ID + " passed"
		s.o.Pub.Publish([]string{alerts.TopicAlerts}, alerts.EventResolved, n)
	}
}

func (s *Service) Decisions(ctx context.Context) ([]*decisions.Decision, error) {
	r, err := s.latest(ctx)
	if err != nil || r == nil || r.Status != StatusFailed {
		return nil, err
	}
	return []*decisions.Decision{{
		ID: "drill:" + r.ID, Kind: decisions.KindAlert, Title: "Restore drill " + r.ID + " failed",
		Summary: r.Error + " (backup " + r.Backup + "); fix the backups, then run POST /api/v1/drills to confirm", CreatedAt: r.FinishedAt,
	}}, nil
}

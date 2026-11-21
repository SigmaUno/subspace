package retention

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/audit"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type sqlTx interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

var errNoSQL = errors.New("retention: store transaction does not expose SQL")

type Maintainer interface {
	SetAutocheckpoint(ctx context.Context, pages int) error
	Checkpoint(ctx context.Context, truncate bool) (int, error)
	Vacuum(ctx context.Context, pages int, full bool) (int, error)
}

type Options struct {
	Store  store.Store
	Policy Policy
	Dir    string
	Now    func() time.Time
	Log    *slog.Logger
}

type Report struct {
	At            time.Time      `json:"at"`
	Resumed       int            `json:"resumed"`
	Archived      []Segment      `json:"archived"`
	Rows          map[Kind]int64 `json:"rows"`
	WALFrames     int            `json:"wal_frames"`
	Checkpointed  bool           `json:"checkpointed"`
	VacuumedPages int            `json:"vacuumed_pages"`
	Error         string         `json:"error,omitempty"`
}

type Status struct {
	Policy   Policy          `json:"policy"`
	Dir      string          `json:"dir"`
	Anchor   model.AuditHead `json:"anchor"`
	Segments []Segment       `json:"segments"`
	LastRun  *Report         `json:"last_run"`
}

type Service struct {
	st    store.Store
	pol   Policy
	dir   string
	now   func() time.Time
	log   *slog.Logger
	fault func(stage string) error

	mu     sync.Mutex
	frames int
	seen   bool
	last   *Report
}

func New(o Options) (*Service, error) {
	if o.Policy == (Policy{}) {
		o.Policy = DefaultPolicy()
	}
	if err := o.Policy.validate(); err != nil {
		return nil, err
	}
	if o.Policy.Dir != "" {
		o.Dir = o.Policy.Dir
	}
	if o.Dir == "" {
		return nil, errors.New("retention: archive directory is required")
	}
	if err := os.MkdirAll(o.Dir, 0o750); err != nil {
		return nil, err
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	o.Policy.Dir = o.Dir
	return &Service{st: o.Store, pol: o.Policy, dir: o.Dir, now: o.Now, log: o.Log}, nil
}

func (s *Service) Run(ctx context.Context) {
	if m, ok := s.st.(Maintainer); ok && s.pol.WALAutocheckpoint > 0 {
		if err := m.SetAutocheckpoint(ctx, s.pol.WALAutocheckpoint); err != nil && ctx.Err() == nil {
			s.log.Error("retention: wal_autocheckpoint", "err", err)
		}
	}
	if s.pol.Interval <= 0 {
		return
	}
	tk := time.NewTicker(time.Duration(s.pol.Interval))
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			rep, err := s.RunOnce(ctx)
			if err != nil {
				if ctx.Err() == nil {
					s.log.Error("retention run failed", "err", err)
				}
				continue
			}
			if len(rep.Archived) > 0 || rep.Resumed > 0 {
				s.log.Info("retention run", "segments", len(rep.Archived), "resumed", rep.Resumed, "rows", rep.Rows)
			}
		}
	}
}

func (s *Service) RunOnce(ctx context.Context) (*Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rep := &Report{At: s.now().UTC(), Archived: []Segment{}, Rows: map[Kind]int64{}}
	err := s.archiveAll(ctx, rep)
	if err == nil {
		err = s.maintain(ctx, rep)
	}
	if err != nil {
		rep.Error = err.Error()
	}
	s.last = rep
	return rep, err
}

func (s *Service) Status(ctx context.Context) (*Status, error) {
	segs, err := listSegments(ctx, s.st)
	if err != nil {
		return nil, err
	}
	var anchor model.AuditHead
	err = s.st.View(ctx, withSQL(func(_ store.Tx, q sqlTx) (e error) { anchor, e = auditAnchor(q); return }))
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	last := s.last
	s.mu.Unlock()
	return &Status{Policy: s.pol, Dir: s.dir, Anchor: anchor, Segments: segs, LastRun: last}, nil
}

func (s *Service) step(stage string) error {
	if s.fault == nil {
		return nil
	}
	return s.fault(stage)
}

func (s *Service) archiveAll(ctx context.Context, rep *Report) error {
	segs, err := listSegments(ctx, s.st)
	if err != nil {
		return err
	}
	for _, seg := range segs {
		if seg.Status != StatusPending {
			continue
		}
		if err := s.prune(ctx, seg); err != nil {
			return err
		}
		rep.Resumed++
	}
	for _, k := range Kinds {
		days := s.pol.Days(k)
		if days == 0 {
			continue
		}
		cutoff := rep.At.Add(-time.Duration(days) * 24 * time.Hour).Truncate(time.Millisecond)
		for {
			seg, err := s.archiveNext(ctx, k, cutoff)
			if err != nil {
				return err
			}
			if seg == nil {
				break
			}
			rep.Archived = append(rep.Archived, *seg)
			rep.Rows[k] += seg.Rows
		}
	}
	return nil
}

func (s *Service) archiveNext(ctx context.Context, k Kind, cutoff time.Time) (*Segment, error) {
	sp := specs[k]
	var (
		rows   []row
		seq    int64
		anchor model.AuditHead
	)
	err := s.st.View(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		var err error
		if seq, err = scalar(q, `SELECT COALESCE(MAX(seq), 0) + 1 FROM retention_segments WHERE kind = ?`, string(k)); err != nil {
			return err
		}
		if k != Audit {
			rows, err = queryRows(q, `SELECT * FROM `+sp.table+` WHERE `+sp.ts+` < ? ORDER BY `+strings.Join(sp.keys, ", ")+` LIMIT ?`, //nolint:gosec // fixed identifiers
				cutoff.UnixMilli(), s.pol.SegmentRows)
			return err
		}
		if anchor, err = auditAnchor(q); err != nil {
			return err
		}
		head, err := scalar(q, `SELECT COALESCE(MAX(id), 0) FROM audit`)
		if err != nil {
			return err
		}
		rows, err = queryRows(q, `SELECT * FROM audit WHERE id > ? AND id < ? ORDER BY id LIMIT ?`, anchor.ID, head, s.pol.SegmentRows)
		n := 0
		for n < len(rows) && rows[n].int("ts") < cutoff.UnixMilli() {
			n++
		}
		rows = rows[:n]
		return err
	}))
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	now := s.now().UTC()
	seg := &Segment{Kind: k, Seq: seq, File: segmentName(k, seq), Rows: int64(len(rows)), Cutoff: cutoff.UTC(),
		Status: StatusPending, CreatedAt: now, UpdatedAt: now}
	if sp.keys[0] == "id" {
		seg.FirstID, seg.LastID = rows[0].int("id"), rows[len(rows)-1].int("id")
	}
	if k == Audit {
		c := audit.Chain{HeadID: anchor.ID, HeadHash: anchor.Hash}
		for _, r := range rows {
			if !c.Add(record(r), r.str("hash")) {
				return nil, model.Conflict("audit chain broken at id %d; refusing to archive", r.int("id"))
			}
		}
		seg.PrevHash, seg.LastHash = anchor.Hash, c.HeadHash
	}
	if seg.Bytes, seg.SHA256, err = writeSegment(s.dir, seg.File, rows); err != nil {
		return nil, err
	}
	if err := s.step("written"); err != nil {
		return nil, err
	}
	err = s.st.Atomic(ctx, withSQL(func(t store.Tx, q sqlTx) error {
		if _, err := q.Exec(`INSERT INTO retention_segments(kind, seq, file, row_count, bytes, sha256, cutoff, first_id, last_id,
			prev_hash, last_hash, status, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			string(seg.Kind), seg.Seq, seg.File, seg.Rows, seg.Bytes, seg.SHA256, seg.Cutoff.UnixMilli(), seg.FirstID, seg.LastID,
			seg.PrevHash, seg.LastHash, seg.Status, now.UnixMilli(), now.UnixMilli()); err != nil {
			return err
		}
		id, err := scalar(q, `SELECT id FROM retention_segments WHERE kind = ? AND seq = ?`, string(seg.Kind), seg.Seq)
		if err != nil {
			return err
		}
		seg.ID = id
		return t.InsertAudit(&model.AuditEvent{TS: now, AgentID: "system", Role: "system", Tool: "retention_archive",
			After: map[string]any{"segment": seg.ID, "kind": string(seg.Kind), "seq": seg.Seq, "rows": seg.Rows,
				"sha256": seg.SHA256, "first_id": seg.FirstID, "last_id": seg.LastID, "cutoff": seg.Cutoff.UnixMilli()}})
	}))
	if err != nil {
		return nil, err
	}
	if err := s.step("manifest"); err != nil {
		return nil, err
	}
	if err := s.prune(ctx, *seg); err != nil {
		return nil, err
	}
	seg.Status = StatusArchived
	return seg, nil
}

func (s *Service) prune(ctx context.Context, seg Segment) error {
	rows, err := readSegment(s.dir, seg)
	if err != nil {
		return err
	}
	sp := specs[seg.Kind]
	where := make([]string, len(sp.keys))
	for i, k := range sp.keys {
		where[i] = k + " = ?"
	}
	del := `DELETE FROM ` + sp.table + ` WHERE ` + strings.Join(where, " AND ") + ` AND ` + sp.ts + ` < ?`
	cutoff := seg.Cutoff.UnixMilli()
	for i := 0; i < len(rows); i += s.pol.Batch {
		chunk := rows[i:min(i+s.pol.Batch, len(rows))]
		err := s.st.Atomic(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
			for _, r := range chunk {
				args := make([]any, 0, len(sp.keys)+1)
				for _, k := range sp.keys {
					args = append(args, r[k])
				}
				if _, err := q.Exec(del, append(args, cutoff)...); err != nil {
					return err
				}
			}
			return nil
		}))
		if err != nil {
			return err
		}
		if err := s.step("delete"); err != nil {
			return err
		}
	}
	return s.st.Atomic(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		_, err := q.Exec(`UPDATE retention_segments SET status = ?, updated_at = ? WHERE id = ?`,
			StatusArchived, s.now().UTC().UnixMilli(), seg.ID)
		return err
	}))
}

func (s *Service) Restore(ctx context.Context, id int64) (*Segment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	segs, err := listSegments(ctx, s.st)
	if err != nil {
		return nil, err
	}
	var seg *Segment
	newest := map[Kind]int64{}
	for i := range segs {
		if segs[i].ID == id {
			seg = &segs[i]
		}
		if segs[i].Status != StatusRestored {
			newest[segs[i].Kind] = max(newest[segs[i].Kind], segs[i].Seq)
		}
	}
	switch {
	case seg == nil:
		return nil, model.NotFound("no retention segment %d", id)
	case seg.Status == StatusRestored:
		return nil, model.Conflict("segment %d is already restored", id)
	case seg.Kind == Audit && seg.Seq != newest[Audit]:
		return nil, model.Conflict("audit segments are restored newest first; restore audit segment seq %d before %d", newest[Audit], seg.Seq)
	}
	rows, err := readSegment(s.dir, *seg)
	if err != nil {
		return nil, err
	}
	table := specs[seg.Kind].table
	for i := 0; i < len(rows); i += s.pol.Batch {
		chunk := rows[i:min(i+s.pol.Batch, len(rows))]
		err := s.st.Atomic(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
			for _, r := range chunk {
				cols := make([]string, 0, len(r))
				for c := range r {
					cols = append(cols, c)
				}
				sort.Strings(cols)
				args := make([]any, len(cols))
				for j, c := range cols {
					args[j] = r[c]
				}
				ins := `INSERT INTO ` + table + `(` + strings.Join(cols, ", ") + `) VALUES(` +
					strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + `) ON CONFLICT DO NOTHING`
				if _, err := q.Exec(ins, args...); err != nil {
					return err
				}
			}
			return nil
		}))
		if err != nil {
			return nil, err
		}
	}
	now := s.now().UTC()
	err = s.st.Atomic(ctx, withSQL(func(t store.Tx, q sqlTx) error {
		if _, err := q.Exec(`UPDATE retention_segments SET status = ?, updated_at = ? WHERE id = ?`, StatusRestored, now.UnixMilli(), seg.ID); err != nil {
			return err
		}
		return t.InsertAudit(&model.AuditEvent{TS: now, AgentID: "system", Role: "system", Tool: "retention_restore",
			After: map[string]any{"segment": seg.ID, "kind": string(seg.Kind), "seq": seg.Seq, "rows": seg.Rows, "sha256": seg.SHA256}})
	}))
	if err != nil {
		return nil, err
	}
	seg.Status, seg.UpdatedAt = StatusRestored, now
	return seg, nil
}

func (s *Service) maintain(ctx context.Context, rep *Report) error {
	m, ok := s.st.(Maintainer)
	if !ok {
		return nil
	}
	frames, err := m.Checkpoint(ctx, false)
	if err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}
	idle := s.seen && frames == s.frames
	if idle && frames > 0 {
		if frames, err = m.Checkpoint(ctx, true); err != nil {
			return fmt.Errorf("checkpoint: %w", err)
		}
		rep.Checkpointed = true
	}
	if s.pol.VacuumPages > 0 {
		n, err := m.Vacuum(ctx, s.pol.VacuumPages, idle)
		if err != nil {
			return fmt.Errorf("vacuum: %w", err)
		}
		rep.VacuumedPages = n
		if n > 0 {
			if frames, err = m.Checkpoint(ctx, false); err != nil {
				return fmt.Errorf("checkpoint: %w", err)
			}
		}
	}
	s.frames, s.seen = frames, true
	rep.WALFrames = frames
	return nil
}

func withSQL(fn func(store.Tx, sqlTx) error) func(store.Tx) error {
	return func(t store.Tx) error {
		q, ok := t.(sqlTx)
		if !ok {
			return errNoSQL
		}
		return fn(t, q)
	}
}

func scalar(q sqlTx, query string, args ...any) (int64, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return 0, errors.Join(rows.Err(), sql.ErrNoRows)
	}
	var n int64
	if err := rows.Scan(&n); err != nil {
		return 0, err
	}
	return n, rows.Err()
}

func queryRows(q sqlTx, query string, args ...any) ([]row, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []row
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := make(row, len(cols))
		for i, c := range cols {
			r[strings.ToLower(c)] = toJSON(vals[i])
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func auditAnchor(q sqlTx) (model.AuditHead, error) {
	rows, err := q.Query(`SELECT last_id, last_hash FROM retention_segments
		WHERE kind = 'audit' AND status IN ('pending', 'archived') ORDER BY last_id DESC LIMIT 1`)
	if err != nil {
		return model.AuditHead{}, err
	}
	defer func() { _ = rows.Close() }()
	var h model.AuditHead
	if rows.Next() {
		if err := rows.Scan(&h.ID, &h.Hash); err != nil {
			return h, err
		}
	}
	return h, rows.Err()
}

const segmentCols = `id, kind, seq, file, row_count, bytes, sha256, cutoff, first_id, last_id, prev_hash, last_hash, status, created_at, updated_at`

func listSegments(ctx context.Context, st store.Store) ([]Segment, error) {
	out := []Segment{}
	err := st.View(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		rows, err := q.Query(`SELECT ` + segmentCols + ` FROM retention_segments ORDER BY kind, seq`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var (
				s                        Segment
				kind                     string
				cutoff, created, updated int64
			)
			if err := rows.Scan(&s.ID, &kind, &s.Seq, &s.File, &s.Rows, &s.Bytes, &s.SHA256, &cutoff, &s.FirstID, &s.LastID,
				&s.PrevHash, &s.LastHash, &s.Status, &created, &updated); err != nil {
				return err
			}
			s.Kind = Kind(kind)
			s.Cutoff, s.CreatedAt, s.UpdatedAt = time.UnixMilli(cutoff).UTC(), time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
			out = append(out, s)
		}
		return rows.Err()
	}))
	return out, err
}

func record(r row) audit.Record {
	return audit.Record{ID: r.int("id"), TS: r.int("ts"), AgentID: r.str("agent_id"), Role: r.str("role"), Tool: r.str("tool"),
		TaskID: r.str("task_id"), Before: r.str("before"), After: r.str("after"), Note: r.str("note")}
}

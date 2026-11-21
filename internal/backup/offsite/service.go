package offsite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/backup"
	"github.com/SigmaUno/subspace/internal/backup/seal"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	StateKey   = "backup.offsite"
	sampleSize = 64 << 10
	stagePart  = ".offsite-*.tmp"
)

type Record struct {
	Object     string    `json:"object"`
	Bytes      int64     `json:"bytes"`
	SHA256     string    `json:"sha256"`
	KeyID      string    `json:"key_id"`
	UploadedAt time.Time `json:"uploaded_at"`
	Verified   bool      `json:"verified"`
	VerifiedAt time.Time `json:"verified_at"`
	Error      string    `json:"error"`
}

type State struct {
	Records       map[string]Record `json:"records"`
	LastAttemptAt time.Time         `json:"last_attempt_at"`
	LastSuccessAt time.Time         `json:"last_success_at"`
	LastError     string            `json:"last_error"`
}

func LoadState(ctx context.Context, st store.Store) (State, error) {
	var out State
	err := st.View(ctx, func(tx store.Tx) error {
		raw, ok, err := tx.GetKV(StateKey)
		if err != nil || !ok {
			return err
		}
		return json.Unmarshal([]byte(raw), &out)
	})
	if out.Records == nil {
		out.Records = map[string]Record{}
	}
	return out, err
}

func (s State) Newest() (name string, rec Record, ok bool) {
	for n, r := range s.Records {
		if r.Verified && (!ok || r.UploadedAt.After(rec.UploadedAt)) {
			name, rec, ok = n, r, true
		}
	}
	return name, rec, ok
}

type Backups interface {
	List(ctx context.Context) ([]backup.Info, error)
}

type Options struct {
	Remote    *Remote
	Store     store.Store
	Backups   Backups
	StageDir  string
	Retention Policy
	Interval  time.Duration
	Now       func() time.Time
	NewTicker func(time.Duration) backup.Ticker
	Log       *slog.Logger
}

type Service struct {
	*Remote
	o  Options
	mu sync.Mutex
}

func NewService(o Options) (*Service, error) {
	if o.Remote == nil || o.Store == nil || o.Backups == nil {
		return nil, errors.New("offsite: Remote, Store and Backups are required")
	}
	if o.StageDir == "" {
		return nil, errors.New("offsite: StageDir is required")
	}
	if o.Retention == (Policy{}) {
		o.Retention = DefaultPolicy
	}
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
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
	o.Log = o.Log.With("component", "offsite", "destination", o.Remote.Describe())
	return &Service{Remote: o.Remote, o: o}, nil
}

func (s *Service) Run(ctx context.Context) {
	tk := s.o.NewTicker(s.o.Interval)
	defer tk.Stop()
	s.o.Log.Info("off-site backups started", "interval", s.o.Interval, "key_id", s.KeyID())
	for {
		if _, err := s.Sync(ctx); err != nil && ctx.Err() == nil {
			s.o.Log.Error("off-site backup failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tk.C():
		}
	}
}

type Result struct {
	Backup   string
	Object   string
	Uploaded bool
	Pruned   []string
}

func (s *Service) Sync(ctx context.Context) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.sync(ctx)
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if serr := s.finish(ctx, res, err); serr != nil {
		err = errors.Join(err, serr)
	}
	return res, err
}

func (s *Service) sync(ctx context.Context) (Result, error) {
	local, err := s.o.Backups.List(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("list local backups: %w", err)
	}
	var newest *backup.Info
	for i := range local {
		if local[i].Verified {
			newest = &local[i]
			break
		}
	}
	if newest == nil {
		return Result{}, nil
	}
	res := Result{Backup: newest.Name, Object: ObjectName(newest.CreatedAt)}
	state, err := LoadState(ctx, s.o.Store)
	if err != nil {
		return res, err
	}
	remote, err := s.List(ctx)
	if err != nil {
		return res, fmt.Errorf("list destination: %w", err)
	}
	present := false
	for _, e := range remote {
		present = present || e.Name == res.Object
	}
	if rec, ok := state.Records[newest.Name]; !ok || !rec.Verified || !present {
		if err := s.upload(ctx, newest, res.Object, &state); err != nil {
			return res, err
		}
		res.Uploaded = true
		if remote, err = s.List(ctx); err != nil {
			return res, fmt.Errorf("list destination: %w", err)
		}
	}
	for _, e := range s.o.Retention.Prune(remote, res.Object) {
		if err := s.dest.Delete(ctx, e.Name); err != nil {
			s.o.Log.Warn("off-site retention could not delete", "object", e.Name, "err", err)
			continue
		}
		res.Pruned = append(res.Pruned, e.Name)
		s.o.Log.Info("old off-site backup removed", "object", e.Name)
	}
	return res, nil
}

func (s *Service) upload(ctx context.Context, b *backup.Info, object string, state *State) (err error) {
	stage, size, sum, err := s.stage(b.Path)
	if err != nil {
		return err
	}
	defer func() {
		_ = stage.Close()
		_ = os.Remove(stage.Name())
	}()
	rec := Record{Object: object, Bytes: size, SHA256: sum, KeyID: s.KeyID(), UploadedAt: s.o.Now()}
	defer func() {
		if err != nil {
			rec.Error = err.Error()
		}
		state.Records[b.Name] = rec
		if serr := s.save(ctx, *state); serr != nil {
			err = errors.Join(err, serr)
		}
	}()
	if err = s.dest.Put(ctx, object, stage, size, sum); err != nil {
		return fmt.Errorf("upload %s: %w", object, err)
	}
	if err = s.verify(ctx, object, stage, size, sum); err != nil {
		return fmt.Errorf("verify %s: %w", object, err)
	}
	rec.Verified, rec.VerifiedAt = true, s.o.Now()
	s.o.Log.Info("backup stored off-site", "backup", b.Name, "object", object, "bytes", size)
	return nil
}

func (s *Service) stage(path string) (f *os.File, size int64, sum string, err error) {
	src, err := os.Open(path) //nolint:gosec // path comes from the backup scheduler
	if err != nil {
		return nil, 0, "", err
	}
	defer func() { _ = src.Close() }()
	if err := os.MkdirAll(s.o.StageDir, 0o750); err != nil {
		return nil, 0, "", err
	}
	stale, _ := filepath.Glob(filepath.Join(s.o.StageDir, stagePart))
	for _, p := range stale {
		_ = os.Remove(p)
	}
	f, err = os.CreateTemp(s.o.StageDir, stagePart)
	if err != nil {
		return nil, 0, "", err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	h := sha256.New()
	cw := &countWriter{w: io.MultiWriter(f, h)}
	if err = seal.Encrypt(cw, src, s.keys[0]); err != nil {
		return nil, 0, "", fmt.Errorf("encrypt %s: %w", filepath.Base(path), err)
	}
	if err = f.Sync(); err != nil {
		return nil, 0, "", err
	}
	return f, cw.n, hex.EncodeToString(h.Sum(nil)), nil
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func (s *Service) verify(ctx context.Context, object string, local *os.File, size int64, sum string) error {
	obj, err := s.dest.Stat(ctx, object)
	if err != nil {
		return err
	}
	if obj.Size != size {
		return fmt.Errorf("size is %d, uploaded %d", obj.Size, size)
	}
	if obj.SHA256 != sum {
		return fmt.Errorf("sha256 is %q, uploaded %q", obj.SHA256, sum)
	}
	n := min(size, sampleSize)
	for _, off := range []int64{0, size - n} {
		got, err := s.dest.ReadRange(ctx, object, off, n)
		if err != nil {
			return fmt.Errorf("read sample at %d: %w", off, err)
		}
		want := make([]byte, n)
		if _, err := local.ReadAt(want, off); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("sample at offset %d differs from the uploaded data", off)
		}
	}
	return nil
}

func (s *Service) finish(ctx context.Context, res Result, syncErr error) error {
	state, err := LoadState(ctx, s.o.Store)
	if err != nil {
		return err
	}
	now := s.o.Now()
	state.LastAttemptAt = now
	state.LastError = ""
	if syncErr != nil {
		state.LastError = syncErr.Error()
	} else if res.Backup != "" {
		state.LastSuccessAt = now
	}
	if len(res.Pruned) > 0 {
		gone := map[string]bool{}
		for _, n := range res.Pruned {
			gone[n] = true
		}
		for name, rec := range state.Records {
			if gone[rec.Object] {
				delete(state.Records, name)
			}
		}
	}
	return s.save(ctx, state)
}

func (s *Service) save(ctx context.Context, state State) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.o.Store.Atomic(ctx, func(tx store.Tx) error { return tx.SetKV(StateKey, string(raw)) })
}

func (s *Service) Annotate(ctx context.Context, list []backup.Info) {
	state, err := LoadState(ctx, s.o.Store)
	if err != nil {
		return
	}
	for i := range list {
		rec, ok := state.Records[list[i].Name]
		if !ok {
			continue
		}
		list[i].Remote = &backup.Remote{
			Object: rec.Object, Bytes: rec.Bytes, UploadedAt: rec.UploadedAt, Verified: rec.Verified, Error: rec.Error,
		}
	}
}

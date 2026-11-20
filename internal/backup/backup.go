package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

const DefaultKeep = 14

const (
	filePrefix = "subspace-"
	fileSuffix = ".db"
	timeLayout = "20060102T150405Z"
)

var (
	namePattern = regexp.MustCompile(`^subspace-(\d{8}T\d{6}Z)\.db$`)

	ErrExists = errors.New("backup already exists")
)

type Ticker interface {
	C() <-chan time.Time
	Stop()
}

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time { return r.t.C }
func (r realTicker) Stop()               { r.t.Stop() }

type Config struct {
	Dir       string
	Interval  time.Duration
	Keep      int
	Run       func(ctx context.Context, dst string) error
	Verify    func(ctx context.Context, path string) error
	Logger    *slog.Logger
	Now       func() time.Time
	NewTicker func(d time.Duration) Ticker
}

type Info struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Bytes      int64     `json:"bytes"`
	Verified   bool      `json:"verified"`
	CreatedAt  time.Time `json:"created_at"`
	AgeSeconds int64     `json:"age_seconds"`
}

type verdict struct {
	size int64
	mod  time.Time
	ok   bool
}

type Scheduler struct {
	cfg Config
	log *slog.Logger

	mu       sync.Mutex
	verdicts map[string]verdict
}

func New(cfg Config) (*Scheduler, error) {
	if cfg.Dir == "" {
		return nil, errors.New("backup: Dir is required")
	}
	if cfg.Run == nil {
		return nil, errors.New("backup: Run is required")
	}
	if cfg.Verify == nil {
		return nil, errors.New("backup: Verify is required")
	}
	if cfg.Keep < 0 {
		return nil, errors.New("backup: Keep must not be negative")
	}
	if cfg.Keep == 0 {
		cfg.Keep = DefaultKeep
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewTicker == nil {
		cfg.NewTicker = func(d time.Duration) Ticker { return realTicker{time.NewTicker(d)} }
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{cfg: cfg, log: log.With("component", "backup"), verdicts: map[string]verdict{}}, nil
}

func (s *Scheduler) Run(ctx context.Context) {
	if s.cfg.Interval <= 0 {
		s.log.Info("automatic backups disabled")
		return
	}
	tk := s.cfg.NewTicker(s.cfg.Interval)
	defer tk.Stop()
	s.log.Info("backup scheduler started", "dir", s.cfg.Dir, "interval", s.cfg.Interval, "keep", s.cfg.Keep)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C():
			if ctx.Err() != nil {
				return
			}
			if _, err := s.RunOnce(ctx); err != nil {
				s.log.Error("backup failed", "err", err)
			}
		}
	}
}

func (s *Scheduler) RunOnce(ctx context.Context) (Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.cfg.Dir, 0o750); err != nil {
		return Info{}, fmt.Errorf("backup: create dir: %w", err)
	}
	now := s.cfg.Now().UTC()
	name := filePrefix + now.Format(timeLayout) + fileSuffix
	dst := filepath.Join(s.cfg.Dir, name)
	if _, err := os.Lstat(dst); err == nil {
		return Info{}, fmt.Errorf("%w: %s", ErrExists, name)
	}
	if err := s.cfg.Run(ctx, dst); err != nil {
		_ = os.Remove(dst)
		return Info{}, fmt.Errorf("backup: write %s: %w", name, err)
	}
	if err := s.cfg.Verify(ctx, dst); err != nil {
		_ = os.Remove(dst)
		return Info{}, fmt.Errorf("backup: verify %s: %w", name, err)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		return Info{}, fmt.Errorf("backup: stat %s: %w", name, err)
	}
	s.verdicts[name] = verdict{size: fi.Size(), mod: fi.ModTime(), ok: true}
	s.log.Info("backup written", "path", dst, "bytes", fi.Size())
	if err := s.prune(); err != nil {
		s.log.Error("backup prune failed", "err", err)
	}
	return s.info(name, fi, now, true), nil
}

func (s *Scheduler) List(ctx context.Context) ([]Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names, err := s.names()
	if err != nil {
		return nil, err
	}
	now := s.cfg.Now().UTC()
	out := make([]Info, 0, len(names))
	for i := len(names) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fi, err := os.Stat(filepath.Join(s.cfg.Dir, names[i]))
		if err != nil {
			continue
		}
		out = append(out, s.info(names[i], fi, now, s.verified(ctx, names[i], fi)))
	}
	return out, nil
}

func (s *Scheduler) info(name string, fi os.FileInfo, now time.Time, verified bool) Info {
	created, _ := parseName(name)
	return Info{
		Name:       name,
		Path:       filepath.Join(s.cfg.Dir, name),
		Bytes:      fi.Size(),
		Verified:   verified,
		CreatedAt:  created,
		AgeSeconds: max(0, int64(now.Sub(created).Seconds())),
	}
}

func (s *Scheduler) verified(ctx context.Context, name string, fi os.FileInfo) bool {
	if v, ok := s.verdicts[name]; ok && v.size == fi.Size() && v.mod.Equal(fi.ModTime()) {
		return v.ok
	}
	err := s.cfg.Verify(ctx, filepath.Join(s.cfg.Dir, name))
	if err != nil {
		s.log.Warn("backup failed verification", "name", name, "err", err)
	}
	s.verdicts[name] = verdict{size: fi.Size(), mod: fi.ModTime(), ok: err == nil}
	return err == nil
}

func parseName(name string) (time.Time, bool) {
	m := namePattern.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.Parse(timeLayout, m[1])
	return t, err == nil
}

func (s *Scheduler) names() ([]string, error) {
	entries, err := os.ReadDir(s.cfg.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("backup: list dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if _, ok := parseName(e.Name()); ok {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (s *Scheduler) prune() error {
	names, err := s.names()
	if err != nil {
		return err
	}
	if len(names) <= s.cfg.Keep {
		return nil
	}
	var errs []error
	for _, n := range names[:len(names)-s.cfg.Keep] {
		if err := os.Remove(filepath.Join(s.cfg.Dir, n)); err != nil {
			errs = append(errs, err)
			continue
		}
		delete(s.verdicts, n)
		s.log.Info("old backup removed", "name", n)
	}
	return errors.Join(errs...)
}

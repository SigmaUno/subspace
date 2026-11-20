// Package backup runs periodic database backups and prunes old ones.
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
	"time"
)

// DefaultKeep is the number of backups retained when Config.Keep is zero.
const DefaultKeep = 14

const (
	filePrefix = "subspace-"
	fileSuffix = ".db"
	timeLayout = "20060102-150405"
)

var namePattern = regexp.MustCompile(`^subspace-\d{8}-\d{6}\.db$`)

// Ticker abstracts time.Ticker so tests can drive the scheduler.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time { return r.t.C }
func (r realTicker) Stop()               { r.t.Stop() }

// Config configures a Scheduler.
type Config struct {
	// Dir receives the backup files. Created if missing.
	Dir string
	// Interval between backups. Run returns immediately when <= 0.
	Interval time.Duration
	// Keep is the number of newest backups retained (default DefaultKeep).
	Keep int
	// Run writes a consistent copy of the database to dstPath
	// (e.g. SQLite VACUUM INTO). dstPath does not exist yet.
	Run func(ctx context.Context, dstPath string) error
	// Verify checks that the written file opens. Optional.
	Verify func(ctx context.Context, path string) error
	// Logger defaults to slog.Default().
	Logger *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
	// NewTicker defaults to a time.Ticker.
	NewTicker func(d time.Duration) Ticker
}

// Scheduler takes a backup every Interval and keeps the newest Keep files.
type Scheduler struct {
	cfg Config
	log *slog.Logger
}

// New validates cfg and returns a Scheduler.
func New(cfg Config) (*Scheduler, error) {
	if cfg.Dir == "" {
		return nil, errors.New("backup: Dir is required")
	}
	if cfg.Run == nil {
		return nil, errors.New("backup: Run is required")
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
	return &Scheduler{cfg: cfg, log: log.With("component", "backup")}, nil
}

// Run blocks, backing up on every tick, until ctx is cancelled.
// Failures are logged and do not stop the scheduler.
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
			s.log.Info("backup scheduler stopped")
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

// RunOnce takes one backup, verifies it, then prunes old backups.
// It returns the path of the new backup. On failure the partial file is removed
// and nothing is pruned.
func (s *Scheduler) RunOnce(ctx context.Context) (string, error) {
	if err := os.MkdirAll(s.cfg.Dir, 0o750); err != nil {
		return "", fmt.Errorf("backup: create dir: %w", err)
	}
	dst := filepath.Join(s.cfg.Dir, filePrefix+s.cfg.Now().UTC().Format(timeLayout)+fileSuffix)
	if _, err := os.Lstat(dst); err == nil {
		return "", fmt.Errorf("backup: %s already exists", dst)
	}
	if err := s.cfg.Run(ctx, dst); err != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("backup: write %s: %w", dst, err)
	}
	if s.cfg.Verify != nil {
		if err := s.cfg.Verify(ctx, dst); err != nil {
			_ = os.Remove(dst)
			return "", fmt.Errorf("backup: verify %s: %w", dst, err)
		}
	}
	var size int64
	if fi, err := os.Stat(dst); err == nil {
		size = fi.Size()
	}
	s.log.Info("backup written", "path", dst, "bytes", size)
	if err := s.Prune(); err != nil {
		s.log.Error("backup prune failed", "err", err)
	}
	return dst, nil
}

// Prune deletes all but the newest Keep backups. Only regular files named
// subspace-YYYYmmdd-HHMMSS.db are considered; everything else is left alone.
func (s *Scheduler) Prune() error {
	entries, err := os.ReadDir(s.cfg.Dir)
	if err != nil {
		return fmt.Errorf("backup: list dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.Type().IsRegular() || !namePattern.MatchString(e.Name()) {
			continue
		}
		if _, err := time.Parse(timeLayout, e.Name()[len(filePrefix):len(e.Name())-len(fileSuffix)]); err != nil {
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) <= s.cfg.Keep {
		return nil
	}
	sort.Strings(names) // fixed-width timestamp: lexical order is chronological
	var errs []error
	for _, n := range names[:len(names)-s.cfg.Keep] {
		p := filepath.Join(s.cfg.Dir, n)
		if err := os.Remove(p); err != nil {
			errs = append(errs, err)
			continue
		}
		s.log.Info("old backup removed", "path", p)
	}
	return errors.Join(errs...)
}

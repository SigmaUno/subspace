package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const (
	lockWait  = 250 * time.Millisecond
	lockStale = 2 * time.Second
	downFor   = 30 * time.Second
)

type rule struct {
	window time.Duration
	max    int
}

var rules = map[string]rule{
	"heartbeat": {time.Minute, 1},
	"log":       {2 * time.Second, 1},
	"status":    {10 * time.Minute, 4},
	"note":      {10 * time.Minute, 4},

	"usage":          {time.Minute, 120},
	"provider-error": {time.Minute, 20},
}

type state struct {
	DownUntil int64              `json:"down_until"`
	Seq       int64              `json:"seq"`
	Calls     map[string][]int64 `json:"calls"`
	Offsets   map[string]int64   `json:"offsets,omitempty"`
}

type limiter struct {
	path string
	now  func() time.Time
}

func newLimiter(dir, url, token string, now func() time.Time) (*limiter, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(url + "\x00" + token))
	return &limiter{path: filepath.Join(dir, hex.EncodeToString(sum[:8])+".json"), now: now}, nil
}

func stateDir(getenv func(string) string) string {
	if d := getenv("SUBSPACE_HOOK_STATE_DIR"); d != "" {
		return d
	}
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "subspace-hook")
	}
	return filepath.Join(os.TempDir(), "subspace-hook")
}

func (l *limiter) reserve(kind string) (seq int64, ok bool) {
	r, known := rules[kind]
	if !known {
		return 0, false
	}
	unlock, locked := l.lock()
	if !locked {
		return 0, false
	}
	defer unlock()
	st := l.load()
	now := l.now().UnixMilli()
	if st.DownUntil > now {
		return 0, false
	}
	kept := st.Calls[kind][:0]
	for _, t := range st.Calls[kind] {
		if t <= now && now-t < r.window.Milliseconds() {
			kept = append(kept, t)
		}
	}
	if len(kept) >= r.max {
		return 0, false
	}
	st.Calls[kind] = append(kept, now)
	st.Seq = max(st.Seq+1, now)
	if l.save(st) != nil {
		return 0, false
	}
	return st.Seq, true
}

func (l *limiter) markDown() {
	unlock, ok := l.lock()
	if !ok {
		return
	}
	defer unlock()
	st := l.load()
	st.DownUntil = l.now().Add(downFor).UnixMilli()
	_ = l.save(st)
}

func (l *limiter) offset(path string) (int64, bool) {
	unlock, ok := l.lock()
	if !ok {
		return 0, false
	}
	defer unlock()
	return l.load().Offsets[path], true
}

func (l *limiter) setOffset(path string, off int64) error {
	unlock, ok := l.lock()
	if !ok {
		return errors.New("state locked")
	}
	defer unlock()
	st := l.load()
	if st.Offsets == nil {
		st.Offsets = map[string]int64{}
	}
	st.Offsets[path] = off
	return l.save(st)
}

func (l *limiter) load() *state {
	st := &state{}
	if b, err := os.ReadFile(l.path); err == nil {
		_ = json.Unmarshal(b, st)
	}
	if st.Calls == nil {
		st.Calls = map[string][]int64{}
	}
	return st
}

func (l *limiter) save(st *state) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

func (l *limiter) lock() (func(), bool) {
	path := l.path + ".lock"
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // path derived from state dir and token digest
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(path) }, true
		}
		if !errors.Is(err, os.ErrExist) || time.Now().After(deadline) {
			return nil, false
		}
		if fi, serr := os.Stat(path); serr == nil && time.Since(fi.ModTime()) > lockStale {
			_ = os.Remove(path)
			continue
		}
		time.Sleep(5 * time.Millisecond)
	}
}

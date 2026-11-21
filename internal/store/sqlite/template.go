package sqlite

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

var (
	template     atomic.Pointer[[]byte]
	templateOnce sync.Once
	templateErr  error
)

func EnableTemplate() error {
	templateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "subspace-sqlite-template-")
		if err != nil {
			templateErr = err
			return
		}
		defer func() { _ = os.RemoveAll(dir) }()
		path := filepath.Join(dir, "template.db")
		s, err := Open(path)
		if err != nil {
			templateErr = err
			return
		}
		if templateErr = s.Close(); templateErr != nil {
			return
		}
		db, err := os.ReadFile(path) //nolint:gosec // path is inside a private temp dir
		if err != nil {
			templateErr = err
			return
		}
		template.Store(&db)
	})
	return templateErr
}

func seedFromTemplate(path string) bool {
	db := template.Load()
	if db == nil {
		return false
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // path is the caller's database path
	if err != nil {
		return false
	}
	_, werr := f.Write(*db)
	if cerr := f.Close(); werr != nil || cerr != nil {
		_ = os.Remove(path)
		return false
	}
	return true
}

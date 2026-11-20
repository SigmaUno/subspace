package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"

	"github.com/SigmaUno/subspace/internal/store"
)

const maxBatch = 128

var extraSchemas []string

func RegisterSchema(ddl string) { extraSchemas = append(extraSchemas, ddl) }

type wreq struct {
	ctx  context.Context
	fn   func(store.Tx) error
	done chan error
}

type Store struct {
	wdb  *sql.DB
	rdb  *sql.DB
	reqs chan *wreq
	quit chan struct{}
	wg   sync.WaitGroup
	once sync.Once
}

var _ store.Store = (*Store)(nil)

func dsn(path string, immediate bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	if immediate {
		q.Set("_txlock", "immediate")
	}
	return "file:" + path + "?" + q.Encode()
}

func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	wdb, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		return nil, err
	}
	wdb.SetMaxOpenConns(1)
	wdb.SetMaxIdleConns(1)
	rdb, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		_ = wdb.Close()
		return nil, err
	}
	rdb.SetMaxOpenConns(16)
	rdb.SetMaxIdleConns(16)
	if _, err := wdb.Exec(schema); err != nil {
		_ = wdb.Close()
		_ = rdb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := migrate(wdb); err != nil {
		_ = wdb.Close()
		_ = rdb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	for _, ddl := range extraSchemas {
		if _, err := wdb.Exec(ddl); err != nil {
			_ = wdb.Close()
			_ = rdb.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	s := &Store{wdb: wdb, rdb: rdb, reqs: make(chan *wreq, 1024), quit: make(chan struct{})}
	s.wg.Add(1)
	go s.writer()
	return s, nil
}

func (s *Store) Close() error {
	var err error
	s.once.Do(func() {
		close(s.quit)
		s.wg.Wait()
		err = errors.Join(s.wdb.Close(), s.rdb.Close())
	})
	return err
}

func (s *Store) Done() <-chan struct{} { return s.quit }

func (t *tx) Exec(q string, args ...any) (sql.Result, error) {
	return t.q.ExecContext(t.ctx, q, args...)
}

func (t *tx) Query(q string, args ...any) (*sql.Rows, error) {
	return t.q.QueryContext(t.ctx, q, args...)
}

func (s *Store) Atomic(ctx context.Context, fn func(store.Tx) error) error {
	r := &wreq{ctx: ctx, fn: fn, done: make(chan error, 1)}
	select {
	case s.reqs <- r:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.quit:
		return errors.New("store closed")
	}
	select {
	case err := <-r.done:
		return err
	case <-s.quit:
		return errors.New("store closed")
	}
}

func (s *Store) View(ctx context.Context, fn func(store.Tx) error) error {
	return fn(&tx{q: s.rdb, ctx: ctx})
}

func (s *Store) Backup(ctx context.Context, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("backup target exists: %s", path)
	}
	_, err := s.rdb.ExecContext(ctx, "VACUUM INTO ?", path)
	return err
}

func (s *Store) writer() {
	defer s.wg.Done()
	for {
		var first *wreq
		select {
		case first = <-s.reqs:
		case <-s.quit:
			return
		}
		batch := []*wreq{first}
	drain:
		for len(batch) < maxBatch {
			select {
			case r := <-s.reqs:
				batch = append(batch, r)
			default:
				break drain
			}
		}
		s.runBatch(batch)
	}
}

func (s *Store) runBatch(batch []*wreq) {
	ctx := context.Background()
	t, err := s.wdb.BeginTx(ctx, nil)
	if err != nil {
		for _, r := range batch {
			r.done <- err
		}
		return
	}
	errs := make([]error, len(batch))
	ok := make([]bool, len(batch))
	for i, r := range batch {
		if cerr := r.ctx.Err(); cerr != nil {
			errs[i] = cerr
			continue
		}
		if _, e := t.ExecContext(ctx, "SAVEPOINT op"); e != nil {
			errs[i] = e
			continue
		}
		e := safeRun(r.fn, &tx{q: t, ctx: ctx})
		if e != nil {
			_, _ = t.ExecContext(ctx, "ROLLBACK TO op")
			_, _ = t.ExecContext(ctx, "RELEASE op")
			errs[i] = e
			continue
		}
		if _, e := t.ExecContext(ctx, "RELEASE op"); e != nil {
			errs[i] = e
			continue
		}
		ok[i] = true
	}
	cerr := t.Commit()
	for i, r := range batch {
		if ok[i] && cerr != nil {
			errs[i] = cerr
		}
		r.done <- errs[i]
	}
}

func safeRun(fn func(store.Tx) error, t store.Tx) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic in write op: %v", p)
		}
	}()
	return fn(t)
}

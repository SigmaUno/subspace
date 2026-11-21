package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"

	"github.com/SigmaUno/subspace/internal/store"
)

const maxBatch = 128

var extraSchemas []string

func RegisterSchema(ddl string) { extraSchemas = append(extraSchemas, ddl) }

type batchOp struct {
	r   *wreq
	err error
}

type wreq struct {
	ctx  context.Context
	fn   func(store.Tx) error
	done chan error
}

type Store struct {
	wdb   *sql.DB
	rdb   *sql.DB
	ws    *stmtCache
	rq    *readQ
	reqs  chan *wreq
	batch []batchOp
	quit  chan struct{}
	wg    sync.WaitGroup
	once  sync.Once
}

var _ store.Store = (*Store)(nil)

var uriPathEscaper = strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")

func dsn(path string, immediate bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Add("_pragma", "temp_store(MEMORY)")
	if immediate {
		q.Set("_txlock", "immediate")
	}
	return "file:" + uriPathEscaper.Replace(path) + "?" + q.Encode()
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
	if err := migrateAuditDeleteTrigger(wdb); err != nil {
		_ = wdb.Close()
		_ = rdb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	s := &Store{wdb: wdb, rdb: rdb, ws: newStmtCache(), rq: &readQ{db: rdb, c: newStmtCache()}, reqs: make(chan *wreq, 1024), quit: make(chan struct{})}
	s.wg.Add(1)
	go s.writer()
	return s, nil
}

func (s *Store) Close() error {
	var err error
	s.once.Do(func() {
		close(s.quit)
		s.wg.Wait()
		s.ws.close()
		s.rq.c.close()
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
		s.wg.Wait()
		select {
		case err := <-r.done:
			return err
		default:
			return errors.New("store closed")
		}
	}
}

func (s *Store) View(ctx context.Context, fn func(store.Tx) error) error {
	return fn(&tx{q: s.rq, ctx: ctx})
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
		select {
		case first := <-s.reqs:
			s.runBatch(first)
		case <-s.quit:
			return
		}
	}
}

func (s *Store) runBatch(first *wreq) {
	ctx := context.Background()
	t, err := s.wdb.BeginTx(ctx, nil)
	if err != nil {
		first.done <- err
		return
	}
	q := &writeQ{tx: t, c: s.ws}
	batch := s.batch[:0]
	limit := min(maxBatch, 1+len(s.reqs))
	for r := first; r != nil; r = s.nextReq(len(batch), limit) {
		batch = append(batch, batchOp{r: r, err: s.runOp(ctx, q, r)})
	}
	cerr := t.Commit()
	for i := range batch {
		op := &batch[i]
		if op.err == nil {
			op.err = cerr
		}
		op.r.done <- op.err
		*op = batchOp{}
	}
	s.batch = batch[:0]
	for qs := range q.miss {
		s.ws.prepare(ctx, s.wdb, qs)
	}
}

func (s *Store) nextReq(n, limit int) *wreq {
	if n >= limit {
		return nil
	}
	select {
	case r := <-s.reqs:
		return r
	default:
		return nil
	}
}

func (s *Store) runOp(ctx context.Context, q *writeQ, r *wreq) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, "SAVEPOINT op"); err != nil {
		return err
	}
	if err := safeRun(r.fn, &tx{q: q, ctx: ctx}); err != nil {
		_, _ = q.ExecContext(ctx, "ROLLBACK TO op")
		_, _ = q.ExecContext(ctx, "RELEASE op")
		return err
	}
	if _, err := q.ExecContext(ctx, "RELEASE op"); err != nil {
		return err
	}
	return nil
}

func safeRun(fn func(store.Tx) error, t store.Tx) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic in write op: %v", p)
		}
	}()
	return fn(t)
}

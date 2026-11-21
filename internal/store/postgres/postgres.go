package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/SigmaUno/subspace/internal/store"
)

const (
	maxBatch     = 128
	readConns    = 48
	connLifetime = 30 * time.Minute
	connIdleTime = 5 * time.Minute
	lockSpace    = 0x53554253
	lockMigrate  = 1
)

var (
	extraSchemas []string
	extraTables  []string
)

func RegisterSchema(ddl string, tables ...string) {
	extraSchemas = append(extraSchemas, ddl)
	extraTables = append(extraTables, tables...)
}

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

func Open(dsn string) (*Store, error) {
	wdb, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	wdb.SetMaxOpenConns(1)
	wdb.SetMaxIdleConns(1)
	wdb.SetConnMaxLifetime(connLifetime)
	rdb, err := sql.Open("pgx", dsn)
	if err != nil {
		_ = wdb.Close()
		return nil, err
	}
	rdb.SetMaxOpenConns(readConns)
	rdb.SetMaxIdleConns(readConns)
	rdb.SetConnMaxLifetime(connLifetime)
	rdb.SetConnMaxIdleTime(connIdleTime)
	if err := migrate(wdb); err != nil {
		_ = wdb.Close()
		_ = rdb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	s := &Store{wdb: wdb, rdb: rdb, reqs: make(chan *wreq, 1024), quit: make(chan struct{})}
	s.wg.Add(1)
	go s.writer()
	return s, nil
}

func migrate(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	t, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = t.Rollback() }()
	if _, err := t.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, $2)`, lockSpace, lockMigrate); err != nil {
		return err
	}
	if _, err := t.ExecContext(ctx, schema); err != nil {
		return err
	}
	for _, tb := range store.CoreTables {
		if _, err := t.ExecContext(ctx, addColumns(tb)); err != nil {
			return err
		}
	}
	for _, ddl := range extraSchemas {
		if _, err := t.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	if err := migrateAuditChain(ctx, t); err != nil {
		return err
	}
	return t.Commit()
}

func addColumns(tb store.Schema) string {
	defs := tb.Defs()
	adds := make([]string, len(defs))
	for i, c := range defs {
		adds[i] = "ADD COLUMN IF NOT EXISTS " + c.Name + " " + pgType(c)
	}
	return "ALTER TABLE " + tb.Table() + " " + strings.Join(adds, ", ")
}

func pgType(c store.ColumnDef) string {
	t := "TEXT"
	switch c.Kind {
	case store.KindInt:
		t = "INTEGER"
	case store.KindMillis:
		t = "BIGINT"
	case store.KindReal:
		t = "DOUBLE PRECISION"
	case store.KindBool:
		t = "BOOLEAN"
	case store.KindJSON:
		t = "JSONB"
	}
	return t + c.Constraint()
}

func migrateAuditChain(ctx context.Context, t *sql.Tx) error {
	var n int
	err := t.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns
  WHERE table_schema = current_schema() AND table_name = 'audit' AND column_name = 'hash'`).Scan(&n)
	if err != nil || n > 0 {
		return err
	}
	for _, q := range []string{
		`ALTER TABLE audit ADD COLUMN hash TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE audit DISABLE TRIGGER audit_no_update`,
	} {
		if _, err := t.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	links, err := (&tx{q: t, ctx: ctx}).chainExisting()
	if err != nil {
		return err
	}
	for _, l := range links {
		if _, err := t.ExecContext(ctx, `UPDATE audit SET hash = $1 WHERE id = $2`, l.Hash, l.ID); err != nil {
			return err
		}
	}
	_, err = t.ExecContext(ctx, `ALTER TABLE audit ENABLE TRIGGER audit_no_update`)
	return err
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
	fail := func(err error) {
		for _, r := range batch {
			r.done <- err
		}
	}
	t, err := s.wdb.BeginTx(ctx, nil)
	if err != nil {
		fail(err)
		return
	}
	if _, err := t.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext(current_schema()))`, lockSpace); err != nil {
		_ = t.Rollback()
		fail(err)
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
		if e == nil {
			_, e = t.ExecContext(ctx, "RELEASE SAVEPOINT op")
		}
		if e != nil {
			_, _ = t.ExecContext(ctx, "ROLLBACK TO SAVEPOINT op")
			_, _ = t.ExecContext(ctx, "RELEASE SAVEPOINT op")
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

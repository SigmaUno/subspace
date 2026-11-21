package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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

	migrateTimeout = 5 * time.Minute

	maxRetries             = 6
	retryBase              = 10 * time.Millisecond
	pgSerializationFailure = "40001"
	pgDeadlockDetected     = "40P01"
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

	readOnly bool
}

var _ store.Store = (*Store)(nil)

func Open(dsn string) (*Store, error) { return OpenWith(dsn, store.OpenOptions{}) }

func OpenWith(dsn string, o store.OpenOptions) (*Store, error) {
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
	readOnly, err := migrate(wdb, o)
	if err != nil {
		_ = wdb.Close()
		_ = rdb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	s := &Store{wdb: wdb, rdb: rdb, reqs: make(chan *wreq, 1024), quit: make(chan struct{}), readOnly: readOnly}
	s.wg.Add(1)
	go s.writer()
	return s, nil
}

const schemaVersionTable = `CREATE TABLE IF NOT EXISTS schema_version (
  id INTEGER PRIMARY KEY CHECK (id = 1), version BIGINT NOT NULL, hash TEXT NOT NULL, updated_at BIGINT NOT NULL
);`

const auditHashDDL = `ALTER TABLE audit ADD COLUMN hash TEXT NOT NULL DEFAULT ''`

func BinarySchema() store.Schema {
	units := append([]string{schema}, extraSchemas...)
	return store.SchemaOf(append(units, auditHashDDL)...)
}

func migrate(db *sql.DB, o store.OpenOptions) (readOnly bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), migrateTimeout)
	defer cancel()
	t, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = t.Rollback() }()
	if _, err := t.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, $2)`, lockSpace, lockMigrate); err != nil {
		return false, err
	}
	stored, err := readSchemaVersion(ctx, t)
	if err != nil {
		return false, err
	}
	bin := BinarySchema()
	act, err := store.CheckSchema(stored, bin, o)
	if err != nil || act == store.SchemaCurrent {
		return false, err
	}
	if act == store.SchemaReadOnly {
		return true, nil
	}
	if _, err := t.ExecContext(ctx, schema); err != nil {
		return false, err
	}
	for _, ddl := range extraSchemas {
		if _, err := t.ExecContext(ctx, ddl); err != nil {
			return false, err
		}
	}
	if err := migrateAuditChain(ctx, t); err != nil {
		return false, err
	}
	if _, err := t.ExecContext(ctx, schemaVersionTable); err != nil {
		return false, err
	}
	if _, err := t.ExecContext(ctx, `INSERT INTO schema_version (id, version, hash, updated_at) VALUES (1, $1, $2, $3)
  ON CONFLICT (id) DO UPDATE SET version = excluded.version, hash = excluded.hash, updated_at = excluded.updated_at`,
		bin.Version, bin.Hash, time.Now().UnixMilli()); err != nil {
		return false, err
	}
	return false, t.Commit()
}

func readSchemaVersion(ctx context.Context, t *sql.Tx) (*store.Schema, error) {
	var exists bool
	if err := t.QueryRowContext(ctx, `SELECT to_regclass('schema_version') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		return nil, err
	}
	var s store.Schema
	err := t.QueryRowContext(ctx, `SELECT version, hash FROM schema_version WHERE id = 1`).Scan(&s.Version, &s.Hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &s, err
}

func migrateAuditChain(ctx context.Context, t *sql.Tx) error {
	var n int
	err := t.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns
  WHERE table_schema = current_schema() AND table_name = 'audit' AND column_name = 'hash'`).Scan(&n)
	if err != nil || n > 0 {
		return err
	}
	for _, q := range []string{
		auditHashDDL,
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

func (s *Store) DatabaseSize(ctx context.Context) (int64, error) {
	var n int64
	err := s.rdb.QueryRowContext(ctx, `SELECT pg_database_size(current_database())`).Scan(&n)
	return n, err
}

func (s *Store) Done() <-chan struct{} { return s.quit }

func (s *Store) ReadOnly() bool { return s.readOnly }

func (s *Store) Atomic(ctx context.Context, fn func(store.Tx) error) error {
	if s.readOnly {
		return store.ErrReadOnly
	}
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
	errs, retry := s.tryBatch(batch)
	if retry && len(batch) > 1 {
		for _, r := range batch {
			s.runBatch([]*wreq{r})
		}
		return
	}
	for attempt := 0; retry && attempt < maxRetries; attempt++ {
		select {
		case <-time.After(retryDelay(attempt)):
		case <-s.quit:
		}
		errs, retry = s.tryBatch(batch)
	}
	for i, r := range batch {
		r.done <- errs[i]
	}
}

func retryDelay(attempt int) time.Duration {
	return retryBase << attempt
}

func retryable(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && (pg.Code == pgSerializationFailure || pg.Code == pgDeadlockDetected)
}

func (s *Store) tryBatch(batch []*wreq) ([]error, bool) {
	ctx := context.Background()
	errs := make([]error, len(batch))
	fail := func(err error) ([]error, bool) {
		for i := range errs {
			errs[i] = err
		}
		return errs, retryable(err)
	}
	t, err := s.wdb.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	if _, err := t.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext(current_schema()))`, lockSpace); err != nil {
		_ = t.Rollback()
		return fail(err)
	}
	ok := make([]bool, len(batch))
	for i, r := range batch {
		if cerr := r.ctx.Err(); cerr != nil {
			errs[i] = cerr
			continue
		}
		if _, e := t.ExecContext(ctx, "SAVEPOINT op"); e != nil {
			errs[i] = e
			if retryable(e) {
				_ = t.Rollback()
				return fail(e)
			}
			continue
		}
		e := safeRun(r.fn, &tx{q: t, ctx: ctx})
		if e == nil {
			_, e = t.ExecContext(ctx, "RELEASE SAVEPOINT op")
		}
		if e != nil {
			if retryable(e) {
				_ = t.Rollback()
				return fail(e)
			}
			_, _ = t.ExecContext(ctx, "ROLLBACK TO SAVEPOINT op")
			_, _ = t.ExecContext(ctx, "RELEASE SAVEPOINT op")
			errs[i] = e
			continue
		}
		ok[i] = true
	}
	if cerr := t.Commit(); cerr != nil {
		if retryable(cerr) {
			return fail(cerr)
		}
		for i := range batch {
			if ok[i] {
				errs[i] = cerr
			}
		}
	}
	return errs, false
}

func safeRun(fn func(store.Tx) error, t store.Tx) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic in write op: %v", p)
		}
	}()
	return fn(t)
}

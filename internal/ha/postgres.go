package ha

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	lockSpace   = 0x53554248
	keySpace    = 0x5355424b
	appPrefix   = "subspace-ha:"
	closeWithin = 2 * time.Second
)

func connect(ctx context.Context, dsn, app string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if app != "" {
		cfg.RuntimeParams["application_name"] = app
	}
	return pgx.ConnectConfig(ctx, cfg)
}

func closeConn(c *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), closeWithin)
	defer cancel()
	_ = c.Close(ctx)
}

type PGLock struct {
	dsn  string
	id   string
	name string
	ttl  time.Duration

	mu   sync.Mutex
	conn *pgx.Conn
}

func NewPGLock(dsn, instance, name string, ttl time.Duration) *PGLock {
	return &PGLock{dsn: dsn, id: instance, name: name, ttl: ttl}
}

func (l *PGLock) session(ctx context.Context) (*pgx.Conn, error) {
	if l.conn != nil {
		return l.conn, nil
	}
	c, err := connect(ctx, l.dsn, appPrefix+l.id)
	if err != nil {
		return nil, err
	}
	if l.ttl > 0 {
		_, _ = c.Exec(ctx, fmt.Sprintf("SET idle_session_timeout = %d", l.ttl.Milliseconds()))
	}
	l.conn = c
	return c, nil
}

func (l *PGLock) drop() {
	if l.conn != nil {
		closeConn(l.conn)
		l.conn = nil
	}
}

func (l *PGLock) Acquire(ctx context.Context) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c, err := l.session(ctx)
	if err != nil {
		return false, err
	}
	var ok bool
	err = c.QueryRow(ctx, `SELECT pg_try_advisory_lock($1, hashtext(current_schema() || ':' || $2))`, lockSpace, l.name).Scan(&ok)
	if err != nil {
		l.drop()
		return false, err
	}
	return ok, nil
}

func (l *PGLock) Renew(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return pgx.ErrTxClosed
	}
	var held bool
	err := l.conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND granted
  AND pid = pg_backend_pid() AND classid::bigint = $1
  AND objid::bigint = (hashtext(current_schema() || ':' || $2)::bigint & 4294967295))`, lockSpace, l.name).Scan(&held)
	if err == nil && !held {
		err = fmt.Errorf("advisory lock %s no longer held", l.name)
	}
	if err != nil {
		l.drop()
	}
	return err
}

func (l *PGLock) Holder(ctx context.Context) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c, err := l.session(ctx)
	if err != nil {
		return "", err
	}
	var app sql.NullString
	err = c.QueryRow(ctx, `SELECT a.application_name FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
  WHERE l.locktype = 'advisory' AND l.granted AND l.classid::bigint = $1
  AND l.objid::bigint = (hashtext(current_schema() || ':' || $2)::bigint & 4294967295)
  AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())`, lockSpace, l.name).Scan(&app)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	if err != nil {
		l.drop()
		return "", err
	}
	return strings.TrimPrefix(app.String, appPrefix), nil
}

func (l *PGLock) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.drop()
}

const (
	keyLockQuery   = `hashtext(current_schema() || ':' || $2)`
	keyLockPollMin = 2 * time.Millisecond
	keyLockPollMax = 50 * time.Millisecond
)

type KeyLocker struct {
	db     *sql.DB
	shards []*lockSession
}

type lockSession struct {
	mu   sync.Mutex
	conn *sql.Conn
	gen  uint64
}

func NewKeyLocker(dsn string, conns int) (*KeyLocker, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(conns)
	db.SetMaxIdleConns(conns)
	k := &KeyLocker{db: db, shards: make([]*lockSession, max(conns, 1))}
	for i := range k.shards {
		k.shards[i] = &lockSession{}
	}
	return k, nil
}

func (k *KeyLocker) Lock(ctx context.Context, key string) (func(), error) {
	sum := sha256.Sum256([]byte(key))
	key = hex.EncodeToString(sum[:])
	s := k.shards[binary.BigEndian.Uint64(sum[:8])%uint64(len(k.shards))]
	wait := keyLockPollMin
	for {
		gen, ok, err := s.try(ctx, k.db, key)
		if err != nil {
			return nil, err
		}
		if ok {
			return func() { s.unlock(gen, key) }, nil
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
		wait = min(2*wait, keyLockPollMax)
	}
}

func (s *lockSession) try(ctx context.Context, db *sql.DB, key string) (uint64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		c, err := db.Conn(ctx)
		if err != nil {
			return 0, false, err
		}
		s.conn = c
		s.gen++
	}
	qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeWithin)
	defer cancel()
	var ok bool
	if err := s.conn.QueryRowContext(qctx, `SELECT pg_try_advisory_lock($1, `+keyLockQuery+`)`, keySpace, key).Scan(&ok); err != nil {
		s.drop()
		return 0, false, err
	}
	return s.gen, ok, nil
}

func (s *lockSession) unlock(gen uint64, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil || s.gen != gen {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), closeWithin)
	defer cancel()
	if _, err := s.conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1, `+keyLockQuery+`)`, keySpace, key); err != nil {
		s.drop()
	}
}

func (s *lockSession) drop() {
	if s.conn == nil {
		return
	}
	_ = s.conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = s.conn.Close()
	s.conn = nil
}

func (k *KeyLocker) Close() error {
	for _, s := range k.shards {
		s.mu.Lock()
		if s.conn != nil {
			_ = s.conn.Close()
			s.conn = nil
		}
		s.mu.Unlock()
	}
	return k.db.Close()
}

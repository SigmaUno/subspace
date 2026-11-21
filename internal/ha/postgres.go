package ha

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
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

type KeyLocker struct {
	db *sql.DB
}

func NewKeyLocker(dsn string, conns int) (*KeyLocker, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(conns)
	db.SetMaxIdleConns(conns)
	db.SetConnMaxIdleTime(time.Minute)
	return &KeyLocker{db: db}, nil
}

func (k *KeyLocker) Lock(ctx context.Context, key string) (func(), error) {
	c, err := k.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	const q = `hashtext(current_schema() || ':' || $2)`
	sum := sha256.Sum256([]byte(key))
	key = hex.EncodeToString(sum[:])
	if _, err := c.ExecContext(ctx, `SELECT pg_advisory_lock($1, `+q+`)`, keySpace, key); err != nil {
		_ = c.Raw(func(any) error { return driver.ErrBadConn })
		_ = c.Close()
		return nil, err
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), closeWithin)
		defer cancel()
		if _, err := c.ExecContext(ctx, `SELECT pg_advisory_unlock($1, `+q+`)`, keySpace, key); err != nil {
			_ = c.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = c.Close()
	}, nil
}

func (k *KeyLocker) Close() error { return k.db.Close() }

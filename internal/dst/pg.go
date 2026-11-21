package dst

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/SigmaUno/subspace/internal/idempotency"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

const pgDSNEnv = "SUBSPACE_TEST_PG_DSN"

type pgSchema struct {
	admin  *sql.DB
	name   string
	dsn    string
	closed bool
}

func newPGSchema() (*pgSchema, error) {
	base := os.Getenv(pgDSNEnv)
	if !strings.HasPrefix(base, "postgres://") && !strings.HasPrefix(base, "postgresql://") {
		return nil, nil
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "subspace_dst_" + hex.EncodeToString(b)
	admin, err := sql.Open("pgx", base)
	if err != nil {
		return nil, err
	}
	if _, err := admin.Exec(`CREATE SCHEMA ` + name); err != nil {
		_ = admin.Close()
		return nil, err
	}
	u, err := url.Parse(base)
	if err != nil {
		_ = admin.Close()
		return nil, err
	}
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	return &pgSchema{admin: admin, name: name, dsn: u.String()}, nil
}

func (p *pgSchema) drop() error {
	if p == nil || p.closed {
		return nil
	}
	p.closed = true
	_, err := p.admin.Exec(`DROP SCHEMA ` + p.name + ` CASCADE`)
	return errors.Join(err, p.admin.Close())
}

type stateStore interface {
	store.Store
	idempotency.Store
}

func (s *BoardSystem) openStore() (stateStore, error) {
	if s.pg != nil {
		return postgres.Open(s.pg.dsn)
	}
	return sqlite.Open(s.path)
}

type backupRow struct {
	Table string          `json:"t"`
	Row   json.RawMessage `json:"r"`
}

func verifyPGBackup(ctx context.Context, path string, want map[string]string) error {
	if err := postgres.VerifyBackup(ctx, path); err != nil {
		return err
	}
	f, err := os.Open(path) //nolint:gosec // path comes from the harness backup directory
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	got := map[string]string{}
	dec := json.NewDecoder(bufio.NewReader(f))
	for {
		var r backupRow
		if err := dec.Decode(&r); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return err
		}
		if r.Table != "tasks" {
			continue
		}
		var t struct{ ID, Status string }
		if err := json.Unmarshal(r.Row, &t); err != nil {
			return err
		}
		got[t.ID] = t.Status
	}
	if !maps.Equal(got, want) {
		return fmt.Errorf("backup tasks %v, live %v", got, want)
	}
	return nil
}

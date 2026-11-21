package testdb

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

func Postgres() bool {
	return strings.HasPrefix(base(), "postgres://") || strings.HasPrefix(base(), "postgresql://")
}

func base() string {
	if v := os.Getenv("SUBSPACE_TEST_DB"); v != "" {
		return v
	}
	return os.Getenv("SUBSPACE_TEST_PG_DSN")
}

func Path(t *testing.T, name string) string {
	t.Helper()
	if !Postgres() {
		return filepath.Join(t.TempDir(), name)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "subspace_test_" + hex.EncodeToString(b)
	admin, err := sql.Open("pgx", base())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	u, err := url.Parse(base())
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

func Open(t *testing.T, path string) store.Store {
	t.Helper()
	var (
		st  store.Store
		err error
	)
	if Postgres() {
		st, err = postgres.Open(path)
	} else {
		st, err = sqlite.Open(path)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func DataDir(t *testing.T) string {
	t.Helper()
	if !Postgres() {
		return ""
	}
	return t.TempDir()
}

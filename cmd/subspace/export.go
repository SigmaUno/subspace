package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

const (
	exportFormat  = "subspace-export"
	schemaVersion = 1
	maxLineBytes  = 64 << 20
)

var (
	identRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
	dsnRe   = regexp.MustCompile(`^[a-z_]+=`)
)

type tableSpec struct{ name, order string }

var coreTables = []tableSpec{
	{"kv", "k"}, {"seq", "name"}, {"briefs", "id"}, {"plans", "id"}, {"agents", "id"}, {"tasks", "id"},
	{"spawns", "id"}, {"questions", "id"}, {"messages", "id"}, {"audit", "id"},
}

type column struct{ name, typ string }

type header struct {
	Format        string        `json:"format"`
	SchemaVersion int           `json:"schema_version"`
	CreatedAt     string        `json:"created_at"`
	Source        string        `json:"source"`
	Tables        []headerTable `json:"tables"`
}

type headerTable struct {
	Name    string   `json:"name"`
	Count   int64    `json:"count"`
	Columns []string `json:"columns"`
}

type line struct {
	Header *header        `json:"header,omitempty"`
	T      string         `json:"t,omitempty"`
	R      map[string]any `json:"r,omitempty"`
	SHA256 string         `json:"sha256,omitempty"`
}

type backend struct {
	postgres bool
	path     string
	db       *sql.DB
}

func isPostgresDSN(s string) bool {
	return strings.HasPrefix(s, "postgres://") || strings.HasPrefix(s, "postgresql://") ||
		dsnRe.MatchString(s)
}

var sqlitePathEscaper = strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")

func openBackend(target string) (*backend, error) {
	if isPostgresDSN(target) {
		st, err := postgres.Open(target)
		if err != nil {
			return nil, err
		}
		if err := st.Close(); err != nil {
			return nil, err
		}
		db, err := sql.Open("pgx", target)
		if err != nil {
			return nil, err
		}
		return &backend{postgres: true, db: db}, nil
	}
	if err := migrateSQLite(target); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "foreign_keys(ON)")
	db, err := sql.Open("sqlite", "file:"+sqlitePathEscaper.Replace(target)+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return &backend{path: target, db: db}, nil
}

func migrateSQLite(path string) error {
	st, err := sqlite.Open(path)
	if err != nil {
		return err
	}
	return st.Close()
}

func (b *backend) close() error { return b.db.Close() }

func (b *backend) kind() string {
	if b.postgres {
		return "postgres"
	}
	return "sqlite"
}

func (b *backend) ph(n int) string {
	if b.postgres {
		return "$" + strconv.Itoa(n)
	}
	return "?"
}

func (b *backend) tables() []tableSpec {
	out := append([]tableSpec(nil), coreTables...)
	if b.postgres {
		for _, name := range postgres.SchemaTables() {
			if !slices.ContainsFunc(coreTables, func(c tableSpec) bool { return c.name == name }) {
				out = append(out, tableSpec{name, "1"})
			}
		}
		return out
	}
	for _, name := range sqlite.ExtensionTables() {
		out = append(out, tableSpec{name, "rowid"})
	}
	return out
}

func quote(name string) string { return `"` + name + `"` }

type queryer interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

func (b *backend) columns(ctx context.Context, q queryer, table string) ([]column, error) {
	query := `SELECT name, type FROM pragma_table_info(?)`
	if b.postgres {
		query = `SELECT column_name, data_type FROM information_schema.columns
  WHERE table_schema = current_schema() AND table_name = $1 ORDER BY ordinal_position`
	}
	rows, err := q.QueryContext(ctx, query, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []column
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.name, &c.typ); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func runExport(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	db := fs.String("db", env("SUBSPACE_DB", "/data/subspace.db"), "database path or postgres DSN")
	out := fs.String("out", "", "output file (.jsonl.gz)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *out == "" {
		return errors.New("usage: subspace export --db <path|dsn> --out file.jsonl.gz")
	}
	if !isPostgresDSN(*db) {
		if _, err := os.Stat(*db); err != nil {
			return fmt.Errorf("database: %w", err)
		}
	}
	if _, err := os.Stat(*out); err == nil {
		return fmt.Errorf("%s exists", *out)
	}
	b, err := openBackend(*db)
	if err != nil {
		return err
	}
	defer func() { _ = b.close() }()
	return exportBackend(context.Background(), b, *out, stdout)
}

func exportBackend(ctx context.Context, b *backend, out string, progress io.Writer) (err error) {
	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		return err
	}
	tmp := out + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // operator-supplied path
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	gz, err := gzip.NewWriterLevel(f, gzip.BestSpeed)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(gz, 1<<20)
	sum := sha256.New()
	w := io.MultiWriter(bw, sum)

	opts := &sql.TxOptions{}
	if b.postgres {
		opts = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	tx, err := b.db.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	specs := b.tables()
	hd := header{Format: exportFormat, SchemaVersion: schemaVersion, CreatedAt: time.Now().UTC().Format(time.RFC3339), Source: b.kind()}
	for _, t := range specs {
		cols, err := b.columns(ctx, tx, t.name)
		if err != nil {
			return fmt.Errorf("%s: %w", t.name, err)
		}
		if len(cols) == 0 {
			return fmt.Errorf("%s: table missing", t.name)
		}
		ht := headerTable{Name: t.name}
		for _, c := range cols {
			ht.Columns = append(ht.Columns, c.name)
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM `+quote(t.name)).Scan(&ht.Count); err != nil {
			return fmt.Errorf("%s: %w", t.name, err)
		}
		hd.Tables = append(hd.Tables, ht)
	}
	if err := writeJSONLine(w, line{Header: &hd}); err != nil {
		return err
	}
	var total int64
	for _, ht := range hd.Tables {
		spec := specFor(specs, ht.Name)
		n, err := exportTable(ctx, tx, w, spec, ht)
		if err != nil {
			return fmt.Errorf("%s: %w", ht.Name, err)
		}
		if n != ht.Count {
			return fmt.Errorf("%s: row count changed during export (%d != %d)", ht.Name, n, ht.Count)
		}
		total += n
		_, _ = fmt.Fprintf(progress, "exported %s: %d rows\n", ht.Name, n)
	}
	trailer, err := json.Marshal(line{SHA256: hex.EncodeToString(sum.Sum(nil))})
	if err != nil {
		return err
	}
	if _, err := bw.Write(append(trailer, '\n')); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, out); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_, err = fmt.Fprintf(progress, "wrote %s (%d rows, %d tables)\n", out, total, len(hd.Tables))
	return err
}

func specFor(specs []tableSpec, name string) tableSpec {
	for _, s := range specs {
		if s.name == name {
			return s
		}
	}
	return tableSpec{name, "1"}
}

func writeJSONLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func exportTable(ctx context.Context, tx *sql.Tx, w io.Writer, spec tableSpec, ht headerTable) (int64, error) {
	quoted := make([]string, len(ht.Columns))
	for i, c := range ht.Columns {
		quoted[i] = quote(c)
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+strings.Join(quoted, ", ")+` FROM `+quote(spec.name)+` ORDER BY `+spec.order) //nolint:gosec // identifiers come from the table registry and the validated header
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	vals := make([]any, len(ht.Columns))
	ptrs := make([]any, len(vals))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	keys := make([][]byte, len(ht.Columns))
	for i, c := range ht.Columns {
		keys[i], _ = json.Marshal(c)
	}
	name, _ := json.Marshal(spec.name)
	var (
		buf bytes.Buffer
		n   int64
	)
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}
		buf.Reset()
		buf.WriteString(`{"t":`)
		buf.Write(name)
		buf.WriteString(`,"r":{`)
		for i, v := range vals {
			if i > 0 {
				buf.WriteByte(',')
			}
			buf.Write(keys[i])
			buf.WriteByte(':')
			cv, err := canonicalValue(spec.name, ht.Columns[i], v)
			if err != nil {
				return n, err
			}
			enc, err := json.Marshal(cv)
			if err != nil {
				return n, err
			}
			buf.Write(enc)
		}
		buf.WriteString("}}\n")
		if _, err := w.Write(buf.Bytes()); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func canonicalValue(table, col string, v any) (any, error) {
	switch x := v.(type) {
	case nil:
		if table == "audit" && (col == "before" || col == "after") {
			return "", nil
		}
		return nil, nil
	case int64, float64, string:
		return x, nil
	case bool:
		if x {
			return int64(1), nil
		}
		return int64(0), nil
	case []byte:
		if !utf8.Valid(x) {
			return nil, fmt.Errorf("column %s holds binary data", col)
		}
		return string(x), nil
	}
	return nil, fmt.Errorf("column %s: unsupported type %T", col, v)
}

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
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/SigmaUno/subspace/internal/audit"
)

const (
	progressEvery = 100000
	maxBatchRows  = 500
	maxBatchArgs  = 30000
)

type archive struct {
	f    *os.File
	gz   *gzip.Reader
	sc   *bufio.Scanner
	sum  hash.Hash
	done bool
}

func openArchive(path string) (*archive, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	return &archive{f: f, gz: gz, sc: sc, sum: sha256.New()}, nil
}

func (a *archive) close() error { return errors.Join(a.gz.Close(), a.f.Close()) }

func (a *archive) next() (*line, error) {
	if a.done {
		return nil, io.EOF
	}
	if !a.sc.Scan() {
		if err := a.sc.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("truncated archive: missing sha256 trailer")
	}
	raw := a.sc.Bytes()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	var ln line
	if err := dec.Decode(&ln); err != nil {
		return nil, fmt.Errorf("malformed line: %w", err)
	}
	if dec.More() {
		return nil, errors.New("malformed line: trailing data")
	}
	if ln.SHA256 == "" {
		a.sum.Write(raw)
		a.sum.Write([]byte{'\n'})
		return &ln, nil
	}
	if got := hex.EncodeToString(a.sum.Sum(nil)); got != ln.SHA256 {
		return nil, fmt.Errorf("checksum mismatch: archive is corrupted or tampered with (trailer %s, computed %s)", ln.SHA256, got)
	}
	a.done = true
	if a.sc.Scan() {
		return nil, errors.New("unexpected data after sha256 trailer")
	}
	return &ln, a.sc.Err()
}

func validateHeader(hd *header) error {
	if hd.Format != exportFormat {
		return fmt.Errorf("not a subspace export (format %q)", hd.Format)
	}
	if hd.SchemaVersion != schemaVersion {
		return fmt.Errorf("unsupported schema version %d (this build reads %d)", hd.SchemaVersion, schemaVersion)
	}
	seen := map[string]bool{}
	for _, t := range hd.Tables {
		if !identRe.MatchString(t.Name) || seen[t.Name] || t.Count < 0 {
			return fmt.Errorf("invalid table %q in header", t.Name)
		}
		seen[t.Name] = true
		cols := map[string]bool{}
		for _, c := range t.Columns {
			if !identRe.MatchString(c) || cols[c] {
				return fmt.Errorf("invalid column %q in table %s", c, t.Name)
			}
			cols[c] = true
		}
		if len(cols) == 0 {
			return fmt.Errorf("table %s has no columns", t.Name)
		}
	}
	return nil
}

func verifyArchive(path string) (*header, error) {
	a, err := openArchive(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = a.close() }()
	first, err := a.next()
	if err != nil {
		return nil, err
	}
	if first.Header == nil {
		return nil, errors.New("first line is not a header")
	}
	hd := first.Header
	if err := validateHeader(hd); err != nil {
		return nil, err
	}
	index := map[string]int{}
	cols := make([]map[string]bool, len(hd.Tables))
	for i, t := range hd.Tables {
		index[t.Name] = i
		cols[i] = map[string]bool{}
		for _, c := range t.Columns {
			cols[i][c] = true
		}
	}
	counts := make([]int64, len(hd.Tables))
	var (
		cur   int
		chain audit.Chain
	)
	for {
		ln, err := a.next()
		if err != nil {
			return nil, err
		}
		if ln.SHA256 != "" {
			break
		}
		i, ok := index[ln.T]
		if ln.Header != nil || !ok || i < cur {
			return nil, fmt.Errorf("unexpected line for table %q", ln.T)
		}
		cur = i
		for k := range ln.R {
			if !cols[i][k] {
				return nil, fmt.Errorf("%s: unexpected column %q", ln.T, k)
			}
		}
		counts[i]++
		if ln.T == "audit" {
			rec, h, err := auditRow(ln.R)
			if err != nil {
				return nil, err
			}
			if !chain.Add(rec, h) {
				return nil, fmt.Errorf("audit hash chain broken at event %d", rec.ID)
			}
		}
	}
	for i, t := range hd.Tables {
		if counts[i] != t.Count {
			return nil, fmt.Errorf("%s: header says %d rows, archive has %d", t.Name, t.Count, counts[i])
		}
	}
	return hd, nil
}

func auditRow(r map[string]any) (audit.Record, string, error) {
	var (
		rec audit.Record
		h   string
		err error
	)
	str := func(k string) string {
		s, ok := r[k].(string)
		if !ok && err == nil {
			err = fmt.Errorf("audit: column %s is not text", k)
		}
		return s
	}
	num := func(k string) int64 {
		n, ok := r[k].(json.Number)
		if !ok {
			if err == nil {
				err = fmt.Errorf("audit: column %s is not a number", k)
			}
			return 0
		}
		v, e := n.Int64()
		if e != nil && err == nil {
			err = fmt.Errorf("audit: column %s: %w", k, e)
		}
		return v
	}
	rec.ID, rec.TS = num("id"), num("ts")
	rec.AgentID, rec.Role, rec.Tool, rec.TaskID = str("agent_id"), str("role"), str("tool"), str("task_id")
	rec.Before, rec.After, rec.Note = str("before"), str("after"), str("note")
	h = str("hash")
	return rec, h, err
}

func runImport(args []string, stdout io.Writer) (err error) {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	db := fs.String("db", env("SUBSPACE_DB", "/data/subspace.db"), "database path or postgres DSN")
	in := fs.String("in", "", "input file (.jsonl.gz)")
	force := fs.Bool("force", false, "replace the contents of a non-empty target")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *in == "" {
		return errors.New("usage: subspace import --db <path|dsn> --in file.jsonl.gz [--force]")
	}
	hd, err := verifyArchive(*in)
	if err != nil {
		return fmt.Errorf("%s: %w", *in, err)
	}
	var rows int64
	for _, t := range hd.Tables {
		rows += t.Count
	}
	_, _ = fmt.Fprintf(stdout, "verified %s: %d rows in %d tables\n", *in, rows, len(hd.Tables))

	if !isPostgresDSN(*db) {
		if err := os.MkdirAll(filepath.Dir(*db), 0o750); err != nil {
			return err
		}
		unlock, err := lockDB(*db)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, unlock()) }()
		if err := ensureIdle(*db); err != nil {
			return err
		}
	}
	b, err := openBackend(*db)
	if err != nil {
		return err
	}
	err = importArchive(context.Background(), b, *in, hd, *force, stdout)
	err = errors.Join(err, b.close())
	if !b.postgres {
		err = errors.Join(err, migrateSQLite(b.path))
	}
	return err
}

func importArchive(ctx context.Context, b *backend, in string, hd *header, force bool, progress io.Writer) error {
	specs := b.tables()
	known := map[string]bool{}
	for _, s := range specs {
		known[s.name] = true
	}
	tcols := map[string][]column{}
	for _, t := range hd.Tables {
		if !known[t.Name] {
			if t.Count > 0 {
				return fmt.Errorf("target (%s) has no table %q", b.kind(), t.Name)
			}
			continue
		}
		cols, err := b.columns(ctx, b.db, t.Name)
		if err != nil {
			return err
		}
		tcols[t.Name] = cols
	}
	if !force {
		var busy []string
		for _, s := range specs {
			var n int
			if err := b.db.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM `+quote(s.name)+` LIMIT 1) x`).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				busy = append(busy, s.name)
			}
		}
		if len(busy) > 0 {
			return fmt.Errorf("target is not empty (%s); use --force to replace it", strings.Join(busy, ", "))
		}
	} else if err := b.clearAll(ctx, specs); err != nil {
		return err
	}

	a, err := openArchive(in)
	if err != nil {
		return err
	}
	defer func() { _ = a.close() }()
	if _, err := a.next(); err != nil {
		return err
	}
	counts := map[string]int64{}
	for _, t := range hd.Tables {
		counts[t.Name] = t.Count
	}
	var cur *tableImport
	for {
		ln, err := a.next()
		if err != nil {
			if cur != nil {
				cur.rollback()
			}
			return importFailed(err)
		}
		if ln.SHA256 != "" {
			break
		}
		if cur == nil || cur.name != ln.T {
			if cur != nil {
				if err := cur.commit(ctx); err != nil {
					return importFailed(err)
				}
			}
			ht := hd.Tables[tableIndex(hd, ln.T)]
			if cur, err = b.beginTable(ctx, ht, tcols[ln.T], progress); err != nil {
				return importFailed(err)
			}
		}
		if err := cur.add(ctx, ln.R); err != nil {
			cur.rollback()
			return importFailed(fmt.Errorf("%s: %w", ln.T, err))
		}
	}
	if cur != nil {
		if err := cur.commit(ctx); err != nil {
			return importFailed(err)
		}
	}
	if err := b.finish(ctx); err != nil {
		return importFailed(err)
	}
	for _, s := range specs {
		var n int64
		if err := b.db.QueryRowContext(ctx, `SELECT count(*) FROM `+quote(s.name)).Scan(&n); err != nil {
			return err
		}
		if n != counts[s.name] {
			return fmt.Errorf("%s: %d rows after import, expected %d", s.name, n, counts[s.name])
		}
	}
	_, err = fmt.Fprintln(progress, "import complete")
	return err
}

func importFailed(err error) error {
	return fmt.Errorf("import failed, the target may be partially written; rerun with --force: %w", err)
}

func tableIndex(hd *header, name string) int {
	for i, t := range hd.Tables {
		if t.Name == name {
			return i
		}
	}
	return -1
}

func (b *backend) clearAll(ctx context.Context, specs []tableSpec) error {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for i := len(specs) - 1; i >= 0; i-- {
		name := specs[i].name
		if name == "audit" {
			stmt := `DROP TRIGGER IF EXISTS audit_no_delete`
			if b.postgres {
				stmt = `ALTER TABLE audit DISABLE TRIGGER audit_no_delete`
			}
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		if name == "retention_segments" && b.postgres {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE retention_segments DISABLE TRIGGER retention_segments_guard`); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+quote(name)); err != nil { //nolint:gosec // identifier comes from the table registry
			return fmt.Errorf("clear %s: %w", name, err)
		}
		if name == "audit" && b.postgres {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE audit ENABLE TRIGGER audit_no_delete`); err != nil {
				return err
			}
		}
		if name == "retention_segments" && b.postgres {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE retention_segments ENABLE TRIGGER retention_segments_guard`); err != nil {
				return err
			}
		}
		if !b.postgres {
			if _, err := tx.ExecContext(ctx, `DELETE FROM sqlite_sequence WHERE name = ?`, name); err != nil && !strings.Contains(err.Error(), "no such table") {
				return err
			}
		}
	}
	return tx.Commit()
}

func (b *backend) finish(ctx context.Context) error {
	if !b.postgres {
		return nil
	}
	rows, err := b.db.QueryContext(ctx, `SELECT table_name, column_name FROM information_schema.columns
  WHERE table_schema = current_schema() AND is_identity = 'YES' ORDER BY table_name`)
	if err != nil {
		return err
	}
	type identity struct{ table, column string }
	var ids []identity
	for rows.Next() {
		var c identity
		if err := rows.Scan(&c.table, &c.column); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, c)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, c := range ids {
		col, tbl := quote(c.column), quote(c.table)
		q := `SELECT setval(pg_get_serial_sequence('` + tbl + `', '` + c.column + `'), COALESCE(max(` + col + `), 1), max(` + col + `) IS NOT NULL) FROM ` + tbl //nolint:gosec // identifiers come from information_schema
		if _, err := b.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

type tableImport struct {
	b        *backend
	name     string
	tx       *sql.Tx
	cols     []column
	full     *sql.Stmt
	batch    int
	args     []any
	pending  int
	written  int64
	total    int64
	progress io.Writer
}

func (b *backend) beginTable(ctx context.Context, ht headerTable, target []column, progress io.Writer) (*tableImport, error) {
	byName := map[string]column{}
	for _, c := range target {
		byName[c.name] = c
	}
	var used, dropped []column
	for _, name := range ht.Columns {
		if c, ok := byName[name]; ok {
			used = append(used, c)
		} else {
			dropped = append(dropped, column{name: name})
		}
	}
	if len(dropped) > 0 {
		names := make([]string, len(dropped))
		for i, c := range dropped {
			names[i] = c.name
		}
		_, _ = fmt.Fprintf(progress, "warning: %s: target has no column %s, values dropped\n", ht.Name, strings.Join(names, ", "))
	}
	if len(used) == 0 {
		return nil, fmt.Errorf("%s: no columns in common with the target", ht.Name)
	}
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	t := &tableImport{b: b, name: ht.Name, tx: tx, cols: used, total: ht.Count, progress: progress,
		batch: max(1, min(maxBatchRows, maxBatchArgs/len(used)))}
	if t.full, err = tx.PrepareContext(ctx, t.insertSQL(t.batch)); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return t, nil
}

func (t *tableImport) insertSQL(rows int) string {
	names := make([]string, len(t.cols))
	for i, c := range t.cols {
		names[i] = quote(c.name)
	}
	var sb strings.Builder
	sb.WriteString(`INSERT INTO ` + quote(t.name) + ` (` + strings.Join(names, ", ") + `) VALUES `)
	n := 0
	for r := 0; r < rows; r++ {
		if r > 0 {
			sb.WriteByte(',')
		}
		sb.WriteByte('(')
		for c := range t.cols {
			if c > 0 {
				sb.WriteByte(',')
			}
			n++
			sb.WriteString(t.b.ph(n))
		}
		sb.WriteByte(')')
	}
	return sb.String()
}

func (t *tableImport) add(ctx context.Context, row map[string]any) error {
	for _, c := range t.cols {
		v, ok := row[c.name]
		if !ok {
			return fmt.Errorf("row misses column %s", c.name)
		}
		cv, err := t.b.convert(t.name, c, v)
		if err != nil {
			return fmt.Errorf("column %s: %w", c.name, err)
		}
		t.args = append(t.args, cv)
	}
	t.pending++
	if t.pending < t.batch {
		return nil
	}
	return t.flush(ctx)
}

func (t *tableImport) flush(ctx context.Context) error {
	if t.pending == 0 {
		return nil
	}
	var err error
	if t.pending == t.batch {
		_, err = t.full.ExecContext(ctx, t.args...)
	} else {
		_, err = t.tx.ExecContext(ctx, t.insertSQL(t.pending), t.args...)
	}
	if err != nil {
		return err
	}
	before := t.written
	t.written += int64(t.pending)
	t.pending, t.args = 0, t.args[:0]
	if t.written/progressEvery != before/progressEvery {
		_, _ = fmt.Fprintf(t.progress, "importing %s: %d/%d\n", t.name, t.written, t.total)
	}
	return nil
}

func (t *tableImport) commit(ctx context.Context) error {
	if err := t.flush(ctx); err != nil {
		t.rollback()
		return fmt.Errorf("%s: %w", t.name, err)
	}
	_ = t.full.Close()
	if err := t.tx.Commit(); err != nil {
		return fmt.Errorf("%s: %w", t.name, err)
	}
	_, _ = fmt.Fprintf(t.progress, "imported %s: %d rows\n", t.name, t.written)
	return nil
}

func (t *tableImport) rollback() {
	_ = t.full.Close()
	_ = t.tx.Rollback()
}

func (b *backend) convert(table string, c column, v any) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		if b.postgres && table == "audit" && x == "" && (c.name == "before" || c.name == "after") {
			return nil, nil
		}
		return x, nil
	case json.Number:
		if b.postgres {
			switch c.typ {
			case "boolean":
				f, err := x.Float64()
				return f != 0, err
			case "double precision", "real", "numeric":
				return x.Float64()
			}
		}
		if n, err := x.Int64(); err == nil {
			return n, nil
		}
		return x.Float64()
	}
	return nil, fmt.Errorf("unsupported value type %T", v)
}

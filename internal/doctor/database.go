package doctor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/SigmaUno/subspace/internal/audit"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

const (
	walWarnBytes  = 64 << 20
	walErrorBytes = 1 << 30
	mib           = 1 << 20
	minFreeBytes  = 512 * mib
)

func dbPath(e Env) string { return getenv(e, "SUBSPACE_DB", "/data/subspace.db") }

func isPostgres(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}

func checkDatabase(ctx context.Context, e Env) []Result {
	path := dbPath(e)
	if isPostgres(path) {
		return result(Info, "db.path", "PostgreSQL store configured, database checks cover SQLite only", "")
	}
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return result(Warn, "db.path", "database %s does not exist yet", "start subspace once to create it, or point SUBSPACE_DB at the right file", path)
	}
	if err != nil {
		return result(Error, "db.path", "cannot stat database: %v", "check the path and permissions", err)
	}
	out := checkPermissions(path, fi)
	db, err := sqlite.OpenReadOnly(path)
	if err != nil {
		return append(out, result(Error, "db.integrity", "cannot open database: %v", restoreHint, err)...)
	}
	defer func() { _ = db.Close() }()
	integrity, readable := checkIntegrity(ctx, db)
	out = append(out, integrity...)
	if !readable {
		return out
	}
	out = append(out, checkForeignKeys(ctx, db)...)
	out = append(out, checkSchema(ctx, db)...)
	out = append(out, checkAuditChain(ctx, db)...)
	return append(out, checkWAL(path)...)
}

const restoreHint = "restore the latest verified backup: subspace restore <backup.db> --force (with the server stopped)"

func checkPermissions(path string, fi os.FileInfo) []Result {
	const id = "db.permissions"
	var out []Result
	if fi.Mode().Perm()&0o077 != 0 {
		out = append(out, result(Warn, id, "database file mode is %04o, it holds token hashes and the audit log", "chmod 600 "+path, fi.Mode().Perm())...)
	}
	dir := filepath.Dir(path)
	if di, err := os.Stat(dir); err == nil && di.Mode().Perm()&0o077 != 0 {
		out = append(out, result(Warn, id, "data directory mode is %04o", "chmod 700 "+dir, di.Mode().Perm())...)
	}
	if len(out) == 0 {
		return result(OK, id, "database file is 0600 and directory is 0700 or stricter", "")
	}
	return out
}

func checkIntegrity(ctx context.Context, db *sql.DB) ([]Result, bool) {
	const id = "db.integrity"
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return result(Error, id, "cannot read database: %v", restoreHint, err), false
	}
	defer func() { _ = rows.Close() }()
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return result(Error, id, "cannot read integrity_check: %v", restoreHint, err), false
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := rows.Err(); err != nil {
		return result(Error, id, "integrity_check failed: %v", restoreHint, err), false
	}
	if len(problems) > 0 {
		return result(Error, id, "integrity_check reports %d problems: %s", restoreHint,
			len(problems), strings.Join(problems[:min(len(problems), 3)], "; ")), true
	}
	return result(OK, id, "integrity_check ok", ""), true
}

func checkForeignKeys(ctx context.Context, db *sql.DB) []Result {
	const id = "db.foreign_keys"
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return result(Error, id, "foreign_key_check failed: %v", "", err)
	}
	defer func() { _ = rows.Close() }()
	n, first := 0, ""
	for rows.Next() {
		var (
			table, parent string
			rowid, fkid   sql.NullInt64
		)
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return result(Error, id, "foreign_key_check failed: %v", "", err)
		}
		if n == 0 {
			first = table + " -> " + parent
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return result(Error, id, "foreign_key_check failed: %v", "", err)
	}
	if n > 0 {
		return result(Error, id, "%d foreign key violations, first %s", restoreHint, n, first)
	}
	return result(OK, id, "no foreign key violations", "")
}

func checkSchema(ctx context.Context, db *sql.DB) []Result {
	const id = "db.schema"
	want, err := sqlite.ExpectedSchema(ctx)
	if err != nil {
		return result(Error, id, "cannot build the expected schema: %v", "", err)
	}
	got, err := sqlite.ReadSchema(ctx, db)
	if err != nil {
		return result(Error, id, "cannot read the schema: %v", "", err)
	}
	var missing []string
	columns := 0
	for table, cols := range want {
		have, ok := got[table]
		if !ok {
			missing = append(missing, table)
			continue
		}
		for _, c := range cols {
			columns++
			if !slices.Contains(have, c) {
				missing = append(missing, table+"."+c)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return result(Error, id, "%d tables or columns from the registered migrations are missing: %s",
			"start the current subspace binary once to apply migrations", len(missing), strings.Join(missing[:min(len(missing), 8)], ", "))
	}
	return result(OK, id, "%d tables and %d columns match the registered migrations", "", len(want), columns)
}

func checkAuditChain(ctx context.Context, db *sql.DB) []Result {
	const id = "db.audit_chain"
	rows, err := db.QueryContext(ctx, `SELECT id, ts, agent_id, role, tool, task_id, before, after, note, hash FROM audit ORDER BY id`)
	if err != nil {
		return result(Error, id, "cannot read the audit log: %v", "", err)
	}
	defer func() { _ = rows.Close() }()
	var c audit.Chain
	for rows.Next() {
		var (
			r    audit.Record
			hash string
		)
		if err := rows.Scan(&r.ID, &r.TS, &r.AgentID, &r.Role, &r.Tool, &r.TaskID, &r.Before, &r.After, &r.Note, &hash); err != nil {
			return result(Error, id, "cannot read the audit log: %v", "", err)
		}
		if !c.Add(r, hash) {
			return result(Error, id, "audit chain broken at id %d after %d verified events", restoreHint, r.ID, c.Events)
		}
	}
	if err := rows.Err(); err != nil {
		return result(Error, id, "cannot read the audit log: %v", "", err)
	}
	return result(OK, id, "audit chain intact, %d events", "", c.Events)
}

func checkWAL(path string) []Result {
	const id = "db.wal"
	fi, err := os.Stat(path + "-wal")
	if err != nil {
		return result(OK, id, "no write-ahead log pending", "")
	}
	const fix = "find long-running readers or transactions, then restart subspace to checkpoint the log"
	switch {
	case fi.Size() >= walErrorBytes:
		return result(Error, id, "write-ahead log is %d MiB", fix, fi.Size()/mib)
	case fi.Size() >= walWarnBytes:
		return result(Warn, id, "write-ahead log is %d MiB", fix, fi.Size()/mib)
	}
	return result(OK, id, "write-ahead log is %d MiB", "", fi.Size()/mib)
}

func checkDisk(_ context.Context, e Env) []Result {
	path := dbPath(e)
	if isPostgres(path) {
		return nil
	}
	dataDir := filepath.Dir(path)
	var size int64
	if fi, err := os.Stat(path); err == nil {
		size = fi.Size()
	}
	out := diskResult(e, "disk.data", dataDir, size)
	backupDir := getenv(e, "SUBSPACE_BACKUP_DIR", filepath.Join(dataDir, "backups"))
	return append(out, diskResult(e, "disk.backup", backupDir, size)...)
}

func diskResult(e Env, id, dir string, dbSize int64) []Result {
	probe := dir
	for {
		if _, err := os.Stat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	free, err := e.DiskFree(probe)
	if err != nil {
		return result(Info, id, "free space of %s unknown: %v", "", dir, err)
	}
	fix := fmt.Sprintf("free disk space on the volume holding %s", dir)
	switch {
	case free < uint64(dbSize): //nolint:gosec
		return result(Error, id, "%d MiB free at %s, less than the %d MiB database", fix, free/mib, dir, dbSize/mib)
	case free < max(uint64(dbSize)*2, minFreeBytes): //nolint:gosec
		return result(Warn, id, "%d MiB free at %s", fix, free/mib, dir)
	}
	return result(OK, id, "%d MiB free at %s", "", free/mib, dir)
}

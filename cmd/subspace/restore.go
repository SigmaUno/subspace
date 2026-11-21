package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/SigmaUno/subspace/internal/backup/offsite"

	_ "modernc.org/sqlite"
)

var requiredTables = []string{"seq", "kv", "briefs", "plans", "tasks", "agents", "messages", "questions", "audit", "spawns"}

func runRestore(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	dbPath := fs.String("db", env("SUBSPACE_DB", "/data/subspace.db"), "database path")
	force := fs.Bool("force", false, "overwrite an existing database")
	fromRemote := fs.Bool("from-remote", false, "download and decrypt a backup from the off-site destination configured in SUBSPACE_OFFSITE_*")
	object := fs.String("name", "", "with --from-remote: remote object to restore (default: the newest)")
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if *fromRemote {
		if len(pos) != 0 {
			return errors.New("usage: subspace restore --from-remote [--name object] [--db path] [--force]")
		}
		return runRestoreRemote(*object, *dbPath, *force, stdout)
	}
	if len(pos) != 1 || *object != "" {
		return errors.New("usage: subspace restore <backup.db> [--db path] [--force] | subspace restore --from-remote [--name object] [--db path] [--force]")
	}
	if err := restore(pos[0], *dbPath, *force); err != nil {
		return err
	}
	_, err := fmt.Fprintf(stdout, "restored %s to %s\n", pos[0], *dbPath)
	return err
}

func runRestoreRemote(object, dbPath string, force bool, stdout io.Writer) error {
	cfg, err := offsite.ConfigFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	if !cfg.Enabled() {
		return errors.New("off-site backups are not configured: set SUBSPACE_OFFSITE_KIND and the settings it needs")
	}
	remote, err := offsite.Open(cfg, offsite.Deps{Getenv: os.Getenv})
	if err != nil {
		return err
	}
	name, err := restoreRemote(context.Background(), remote, object, dbPath, force)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "restored %s from %s to %s\n", name, remote.Describe(), dbPath)
	return err
}

func restoreRemote(ctx context.Context, remote *offsite.Remote, object, dbPath string, force bool) (name string, err error) {
	if object == "" {
		e, err := remote.Latest(ctx)
		if err != nil {
			return "", err
		}
		object = e.Name
	}
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".restore-remote-*.db")
	if err != nil {
		return "", err
	}
	defer func() {
		_ = tmp.Close()
		err = errors.Join(err, removeIfExists(tmp.Name()))
	}()
	if _, err := remote.Download(ctx, object, tmp); err != nil {
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", err
	}
	if err := restore(tmp.Name(), dbPath, force); err != nil {
		return "", err
	}
	return object, nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func restore(backup, dbPath string, force bool) (err error) {
	if err := verifyDB(backup); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		return err
	}
	unlock, err := lockDB(dbPath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	if err := ensureIdle(dbPath); err != nil {
		return err
	}
	if _, err := os.Stat(dbPath); err == nil && !force {
		return fmt.Errorf("%s exists; use --force to overwrite", dbPath)
	}

	tmp := dbPath + ".restore"
	if err := copyFile(backup, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := verifyDB(tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("copy: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(dbPath + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, dbPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func lockDB(dbPath string) (func() error, error) {
	f, err := os.OpenFile(dbPath+".lock", os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // operator-supplied path
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { //nolint:gosec // fd fits int
		_ = f.Close()
		return nil, fmt.Errorf("%s is locked by another process", dbPath)
	}
	return f.Close, nil
}

func ensureIdle(dbPath string) error {
	if fi, err := os.Stat(dbPath + "-wal"); err == nil && fi.Size() > 0 {
		return fmt.Errorf("%s-wal has pending data: database is in use or was not closed cleanly; stop the server first", dbPath)
	}
	if _, err := os.Stat(dbPath + "-shm"); err == nil {
		return fmt.Errorf("%s-shm exists: database is in use; stop the server first", dbPath)
	}
	return nil
}

func verifyDB(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	q := url.Values{}
	q.Set("mode", "ro")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query("PRAGMA integrity_check")
	if err != nil {
		return err
	}
	var problems []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			_ = rows.Close()
			return err
		}
		if s != "ok" {
			problems = append(problems, s)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("integrity check failed: %s", strings.Join(problems, "; "))
	}
	for _, name := range requiredTables {
		var n int
		err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("missing table %q: not a subspace database", name)
		}
	}
	return nil
}

func copyFile(src, dst string) (err error) {
	in, err := os.Open(src) //nolint:gosec // operator-supplied path
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // operator-supplied path
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

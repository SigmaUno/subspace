package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

func runBackup(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	dbPath := fs.String("db", env("SUBSPACE_DB", "/data/subspace.db"), "database path")
	out := fs.String("out", "", "backup file (default <backup dir>/subspace-<timestamp>.db)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if _, err := os.Stat(*dbPath); err != nil {
		return fmt.Errorf("database: %w", err)
	}
	target := *out
	if target == "" {
		dir := env("SUBSPACE_BACKUP_DIR", filepath.Join(filepath.Dir(*dbPath), "backups"))
		target = filepath.Join(dir, "subspace-"+time.Now().UTC().Format("20060102T150405Z")+".db")
	}
	st, err := sqlite.Open(*dbPath)
	if err != nil {
		return err
	}
	if err := st.Backup(context.Background(), target); err != nil {
		return errors.Join(err, st.Close())
	}
	if err := st.Close(); err != nil {
		return err
	}
	fi, err := os.Stat(target)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s %d bytes\n", target, fi.Size())
	return err
}

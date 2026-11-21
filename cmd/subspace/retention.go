package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/SigmaUno/subspace/internal/retention"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

func archiveDir(dbPath string) string {
	return env("SUBSPACE_RETAIN_DIR", filepath.Join(filepath.Dir(dbPath), "retention"))
}

func runRetentionRestore(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("retention-restore", flag.ContinueOnError)
	dbPath := fs.String("db", env("SUBSPACE_DB", "/data/subspace.db"), "database path")
	dir := fs.String("dir", "", "archive directory (default SUBSPACE_RETAIN_DIR or <db dir>/retention)")
	segment := fs.Int64("segment", 0, "restore one segment by id")
	kind := fs.String("kind", "", "restore every archived segment of this kind (audit, messages, usage, idempotency, transcripts), newest first")
	list := fs.Bool("list", false, "list segments")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if !*list && (*segment == 0) == (*kind == "") {
		return errors.New("give exactly one of -segment, -kind or -list")
	}
	if *kind != "" && !slices.Contains(retention.Kinds, retention.Kind(*kind)) {
		return fmt.Errorf("unknown kind %q", *kind)
	}
	if _, err := os.Stat(*dbPath); err != nil {
		return fmt.Errorf("database: %w", err)
	}
	if *dir == "" {
		*dir = archiveDir(*dbPath)
	}
	st, err := sqlite.Open(*dbPath)
	if err != nil {
		return err
	}
	svc, err := retention.New(retention.Options{Store: st, Dir: *dir})
	if err == nil {
		err = restoreSegments(context.Background(), svc, *segment, retention.Kind(*kind), *list, stdout)
	}
	return errors.Join(err, st.Close())
}

func restoreSegments(ctx context.Context, svc *retention.Service, id int64, kind retention.Kind, list bool, stdout io.Writer) error {
	status, err := svc.Status(ctx)
	if err != nil {
		return err
	}
	if list {
		for _, s := range status.Segments {
			if _, err := fmt.Fprintf(stdout, "%d %s seq %d %s rows %d %s\n", s.ID, s.Kind, s.Seq, s.Status, s.Rows, s.File); err != nil {
				return err
			}
		}
		return nil
	}
	ids := []int64{id}
	if kind != "" {
		ids = nil
		for _, s := range slices.Backward(status.Segments) {
			if s.Kind == kind && s.Status != retention.StatusRestored {
				ids = append(ids, s.ID)
			}
		}
	}
	for _, id := range ids {
		s, err := svc.Restore(ctx, id)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "restored segment %d %s seq %d rows %d\n", s.ID, s.Kind, s.Seq, s.Rows); err != nil {
			return err
		}
	}
	return nil
}

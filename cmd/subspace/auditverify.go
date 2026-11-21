package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/SigmaUno/subspace/internal/retention"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

func runAuditVerify(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("audit-verify", flag.ContinueOnError)
	dbPath := fs.String("db", env("SUBSPACE_DB", "/data/subspace.db"), "database path")
	dir := fs.String("archive", "", "retention archive directory (default SUBSPACE_RETAIN_DIR or <db dir>/retention)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if _, err := os.Stat(*dbPath); err != nil {
		return fmt.Errorf("database: %w", err)
	}
	st, err := sqlite.Open(*dbPath)
	if err != nil {
		return err
	}
	if *dir == "" {
		*dir = archiveDir(*dbPath)
	}
	v, err := retention.Verify(context.Background(), st, *dir)
	if err = errors.Join(err, st.Close()); err != nil {
		return err
	}
	c := v.Chain
	switch {
	case v.BrokenSegment != 0:
		return fmt.Errorf("archived segment %d: %s", v.BrokenSegment, v.Error)
	case !c.OK:
		return fmt.Errorf("audit chain broken at id %d (%d events verified before it)", c.BrokenID, c.Events)
	}
	if _, err := fmt.Fprintf(stdout, "ok %d events head %d %s\n", c.Events, c.Head.ID, c.Head.Hash); err != nil || v.Segments == 0 {
		return err
	}
	_, err = fmt.Fprintf(stdout, "archive ok %d segments %d rows anchor %d %s\n", v.Segments, v.Rows, v.Anchor.ID, v.Anchor.Hash)
	return err
}

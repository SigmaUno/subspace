package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

func runAuditVerify(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("audit-verify", flag.ContinueOnError)
	dbPath := fs.String("db", env("SUBSPACE_DB", "/data/subspace.db"), "database path")
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
	var c model.AuditCheck
	err = st.View(context.Background(), func(tx store.Tx) (e error) { c, e = tx.VerifyAudit(); return })
	if err = errors.Join(err, st.Close()); err != nil {
		return err
	}
	if !c.OK {
		return fmt.Errorf("audit chain broken at id %d (%d events verified before it)", c.BrokenID, c.Events)
	}
	_, err = fmt.Fprintf(stdout, "ok %d events head %d %s\n", c.Events, c.Head.ID, c.Head.Hash)
	return err
}

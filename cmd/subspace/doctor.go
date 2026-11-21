package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/SigmaUno/subspace/internal/doctor"
)

func runDoctor(args []string, stdout io.Writer) error {
	return runDoctorEnv(args, stdout, doctor.SystemEnv())
}

func runDoctorEnv(args []string, stdout io.Writer, e doctor.Env) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report := doctor.Run(ctx, e)
	write := report.WriteText
	if *asJSON {
		write = report.WriteJSON
	}
	if err := write(stdout); err != nil {
		return err
	}
	if report.Errors > 0 {
		return fmt.Errorf("doctor found %d errors", report.Errors)
	}
	return nil
}

package main

import (
	"context"
	"io"

	"github.com/SigmaUno/subspace/internal/doctor"
)

var doctorEnv = doctor.SystemEnv

func parseDoctor(args []string, _ io.Reader) (command, error) {
	if err := parseFlags(flags("doctor"), args, 0, 0); err != nil {
		return nil, err
	}
	return func(ctx context.Context, _ *conn, p *printer) error {
		report := doctor.Run(ctx, doctorEnv())
		var err error
		if p.json {
			err = p.value(report)
		} else {
			err = report.WriteText(p.w)
		}
		if err != nil {
			return err
		}
		if report.Errors > 0 {
			return failf(exitFailure, "doctor found %d errors", report.Errors)
		}
		return nil
	}, nil
}

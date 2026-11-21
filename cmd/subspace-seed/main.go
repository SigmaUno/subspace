package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type options struct {
	url   string
	token string
	seed  int64
	live  bool
	tick  time.Duration
	demo  bool
	addr  string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("subspace-seed", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o options
	fs.StringVar(&o.url, "url", "http://localhost:8080", "base URL of an empty Subspace server")
	fs.StringVar(&o.token, "token", os.Getenv("SUBSPACE_OWNER_TOKEN"), "owner token (default $SUBSPACE_OWNER_TOKEN)")
	fs.Int64Var(&o.seed, "seed", 1, "seed for all generated variation")
	fs.BoolVar(&o.live, "live", false, "keep agents progressing tasks after seeding until interrupted")
	fs.DurationVar(&o.tick, "tick", 2*time.Second, "pause between simulator steps with -live")
	fs.BoolVar(&o.demo, "demo", false, "start a temporary server with a random owner token, then seed it")
	fs.StringVar(&o.addr, "addr", "127.0.0.1:8080", "listen address with -demo")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected arguments: %v\n", fs.Args())
		return 2
	}
	if !o.demo && o.token == "" {
		_, _ = fmt.Fprintln(stderr, "subspace-seed: -token or SUBSPACE_OWNER_TOKEN is required")
		return 2
	}
	if o.tick <= 0 {
		_, _ = fmt.Fprintln(stderr, "subspace-seed: -tick must be positive")
		return 2
	}
	var err error
	if o.demo {
		err = runDemo(ctx, o, stdout, stderr)
	} else {
		err = seedAndSimulate(ctx, o, o.url, o.token, stdout)
	}
	if err != nil && ctx.Err() == nil {
		_, _ = fmt.Fprintf(stderr, "subspace-seed: %v\n", err)
		return 1
	}
	return 0
}

func seedAndSimulate(ctx context.Context, o options, url, token string, out io.Writer) error {
	s := NewSeeder(url, token, o.seed, out)
	if err := s.Run(ctx); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "seeded")
	if !o.live {
		return nil
	}
	_, _ = fmt.Fprintf(out, "simulating agents every %s, Ctrl-C to stop\n", o.tick)
	return NewSim(s, o.seed).Run(ctx, o.tick)
}

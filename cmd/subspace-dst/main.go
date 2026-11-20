package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/SigmaUno/subspace/internal/dst"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("subspace-dst", flag.ContinueOnError)
	fs.SetOutput(stderr)
	seed := fs.Int64("seed", 0, "first seed (default random)")
	seeds := fs.Int("seeds", 1, "number of consecutive seeds to run")
	steps := fs.Int("steps", 200, "steps per seed")
	agents := fs.Int("agents", 8, "simulated agents: leader, tester, two reviewers and workers (min 6)")
	shrink := fs.Int("shrink", 150, "maximum reruns spent minimizing a failing step log")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected arguments: %v\n", fs.Args())
		return 2
	}
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == "seed" })
	if !set {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			_, _ = fmt.Fprintf(stderr, "seed: %v\n", err)
			return 2
		}
		*seed = int64(binary.LittleEndian.Uint64(b[:]) >> 1) //nolint:gosec
	}
	for i := 0; i < *seeds; i++ {
		cfg := dst.Config{Seed: *seed + int64(i), Steps: *steps, Agents: *agents}
		_, _ = fmt.Fprintf(stdout, "seed %d steps %d agents %d: ", cfg.Seed, cfg.Steps, cfg.Agents)
		res := dst.Check(ctx, cfg)
		if res.Err == nil {
			_, _ = fmt.Fprintln(stdout, "ok")
			continue
		}
		_, _ = fmt.Fprintf(stdout, "FAIL\n%v\n", res.Err)
		if ctx.Err() != nil {
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "minimizing (%d of %d steps)...\n", len(res.Ops), cfg.Steps)
		small := dst.Shrink(ctx, res, *shrink)
		_, _ = fmt.Fprintf(stdout, "minimal reproduction: %d steps, %v\n", len(small.Ops), small.Err)
		for _, l := range small.Log {
			_, _ = fmt.Fprintln(stdout, l)
		}
		_, _ = fmt.Fprintf(stdout, "reproduce: go run ./cmd/subspace-dst -seed %d -steps %d -agents %d\n", cfg.Seed, cfg.Steps, cfg.Agents)
		return 1
	}
	return 0
}

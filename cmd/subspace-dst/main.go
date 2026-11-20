package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sync"

	"github.com/SigmaUno/subspace/internal/dst"
)

func main() {
	os.Exit(run())
}

func run() int {
	seeds := flag.Int("seeds", 100, "number of seeds to run")
	steps := flag.Int("steps", 500, "steps per seed")
	start := flag.Int64("start", 1, "first seed")
	parallel := flag.Int("parallel", runtime.GOMAXPROCS(0), "seeds run concurrently")
	shrink := flag.Int("shrink", 400, "max replays when minimizing a failing trace (0 = off)")
	regressions := flag.String("regressions", "testdata/dst/regressions.txt", "file failing seeds are appended to (empty = off)")
	flag.Parse()
	if *seeds < 1 || *steps < 1 || *parallel < 1 {
		fmt.Fprintln(os.Stderr, "seeds, steps and parallel must be >= 1")
		return 2
	}

	ctx := context.Background()
	r := &dst.Runner{Factory: dst.NewBoardSystem}
	outs := make([]*dst.Outcome, *seeds)
	errs := make([]error, *seeds)
	next := make(chan int)
	var wg sync.WaitGroup
	for range *parallel {
		wg.Go(func() {
			for i := range next {
				o, err := r.Run(ctx, *start+int64(i), *steps)
				if err == nil && o.Failed() && *shrink > 0 {
					o, err = r.Shrink(ctx, o, *shrink)
				}
				outs[i], errs[i] = o, err
			}
		})
	}
	for i := range *seeds {
		next <- i
	}
	close(next)
	wg.Wait()

	failed := 0
	for i, o := range outs {
		seed := *start + int64(i)
		if errs[i] != nil {
			fmt.Fprintf(os.Stderr, "seed %d: %v\n", seed, errs[i])
			return 2
		}
		if !o.Failed() {
			continue
		}
		failed++
		fmt.Printf("FAIL seed=%d invariant=%s\nreplay: go run ./cmd/subspace-dst -seeds 1 -start %d -steps %d\nminimal trace (%d steps):\n",
			seed, o.Violation.Invariant, seed, *steps, len(o.Steps))
		o.WriteTrace(os.Stdout)
		if *regressions != "" {
			if err := dst.AppendRegression(*regressions, dst.Regression{Seed: seed, Steps: *steps}, o.Violation.Invariant); err != nil {
				fmt.Fprintf(os.Stderr, "regressions: %v\n", err)
				return 2
			}
		}
	}
	fmt.Printf("%d seeds [%d..%d], %d steps each: %d failed\n", *seeds, *start, *start+int64(*seeds)-1, *steps, failed)
	if failed > 0 {
		return 1
	}
	return 0
}

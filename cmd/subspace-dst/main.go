package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/SigmaUno/subspace/internal/dst"
)

func main() {
	os.Exit(run())
}

type sweep struct {
	r           *dst.Runner
	mode        string
	steps       int
	parallel    int
	shrink      int
	regressions string
	logDir      string
	filer       *filer
}

func run() int {
	seeds := flag.Int("seeds", 100, "number of seeds to run (per round with -loop)")
	steps := flag.Int("steps", 500, "steps per seed")
	start := flag.Int64("start", 1, "first seed")
	parallel := flag.Int("parallel", runtime.GOMAXPROCS(0), "seeds run concurrently")
	shrink := flag.Int("shrink", 400, "max replays when minimizing a failing step log (0 = off)")
	regressions := flag.String("regressions", "testdata/dst/regressions.txt", "file failing seeds are appended to (empty = off)")
	mode := flag.String("mode", dst.ModeBoard, "system under test: board or server (full wired server)")
	logDir := flag.String("log", "", "directory minimal failing step logs are written to (empty = off)")
	minimize := flag.String("minimize", "", "step log to minimize, replayed with seed -start")
	serial := flag.Bool("serial", false, "disable concurrent batches")
	leaders := flag.Bool("leaders", false, "run two leaders with brief handoffs")
	loop := flag.Bool("loop", false, "keep running rounds of -seeds fresh seeds until interrupted")
	url := flag.String("url", os.Getenv("SUBSPACE_URL"), "Subspace base URL findings are filed to (default $SUBSPACE_URL)")
	fileBrief := flag.String("file-brief", "", "file each new failing seed as a task in this brief, as the leader in SUBSPACE_TOKEN_FILE or SUBSPACE_TOKEN (empty = off)")
	fileRepo := flag.String("file-repo", "", "repo of filed tasks (default: the brief's first repo)")
	flag.Parse()
	if *seeds < 1 || *steps < 1 || *parallel < 1 {
		fmt.Fprintln(os.Stderr, "seeds, steps and parallel must be >= 1")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s := &sweep{mode: *mode, steps: *steps, parallel: *parallel, shrink: *shrink, regressions: *regressions, logDir: *logDir}
	switch *mode {
	case dst.ModeBoard:
		s.r = &dst.Runner{Factory: dst.NewBoardSystem}
	case dst.ModeServer:
		s.r = dst.ServerRunner("")
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q\n", *mode)
		return 2
	}
	s.r.Serial, s.r.Leaders = *serial, *leaders
	if *minimize != "" {
		return minimizeLog(context.Background(), s.r, *minimize, *start, *shrink)
	}
	if *fileBrief != "" {
		token, err := secret("SUBSPACE_TOKEN")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		if s.filer, err = newFiler(ctx, *url, token, *fileBrief, *fileRepo); err != nil {
			fmt.Fprintf(os.Stderr, "file: %v\n", err)
			return 2
		}
	}

	failed := 0
	for first := *start; ; first += int64(*seeds) {
		n, err := s.round(ctx, first, *seeds)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		failed += n
		if !*loop || ctx.Err() != nil {
			break
		}
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func secret(name string) (string, error) {
	if f := os.Getenv(name + "_FILE"); f != "" {
		b, err := os.ReadFile(f) //nolint:gosec // operator-supplied path
		if err != nil {
			return "", fmt.Errorf("read %s_FILE: %w", name, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return strings.TrimSpace(os.Getenv(name)), nil
}

func (s *sweep) round(ctx context.Context, first int64, seeds int) (int, error) {
	outs := make([]*dst.Outcome, seeds)
	errs := make([]error, seeds)
	next := make(chan int)
	var wg sync.WaitGroup
	for range s.parallel {
		wg.Go(func() {
			for i := range next {
				o, err := s.r.Run(context.Background(), first+int64(i), s.steps)
				if err == nil && o.Failed() && s.shrink > 0 {
					o, err = s.r.Minimize(context.Background(), o, s.shrink)
				}
				outs[i], errs[i] = o, err
			}
		})
	}
	ran := 0
dispatch:
	for ; ran < seeds; ran++ {
		select {
		case next <- ran:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(next)
	wg.Wait()

	failed := 0
	for i, o := range outs[:ran] {
		seed := first + int64(i)
		if errs[i] != nil {
			return failed, fmt.Errorf("seed %d: %w", seed, errs[i])
		}
		if !o.Failed() {
			continue
		}
		failed++
		if err := s.report(seed, o); err != nil {
			return failed, err
		}
	}
	if ran > 0 {
		fmt.Printf("%s: %d seeds [%d..%d], %d steps each: %d failed\n", s.mode, ran, first, first+int64(ran)-1, s.steps, failed)
	}
	return failed, nil
}

func (s *sweep) report(seed int64, o *dst.Outcome) error {
	fmt.Printf("FAIL seed=%d invariant=%s\nreplay: go run ./cmd/subspace-dst -mode %s -seeds 1 -start %d -steps %d\nminimal trace (%d steps):\n",
		seed, o.Violation.Invariant, s.mode, seed, s.steps, len(o.Steps))
	o.WriteTrace(os.Stdout)
	if s.logDir != "" {
		path := filepath.Join(s.logDir, fmt.Sprintf("seed-%d.jsonl", seed))
		if err := writeLog(path, o); err != nil {
			return fmt.Errorf("log: %w", err)
		}
		fmt.Printf("step log: %s\nminimize: go run ./cmd/subspace-dst -mode %s -start %d -minimize %s\n", path, s.mode, seed, path)
	}
	if s.regressions != "" {
		if err := dst.AppendRegression(s.regressions, dst.Regression{Seed: seed, Steps: s.steps, Mode: s.mode}, o.Violation.Invariant); err != nil {
			return fmt.Errorf("regressions: %w", err)
		}
	}
	if s.filer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		t, err := s.filer.file(ctx, s.mode, s.steps, o)
		if err != nil {
			return fmt.Errorf("file seed %d: %w", seed, err)
		}
		if t != nil {
			fmt.Printf("filed: %s %s\n", t.ID, t.Title)
		}
	}
	return nil
}

func writeLog(path string, o *dst.Outcome) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	f, err := os.Create(filepath.Clean(path))
	if err != nil {
		return err
	}
	if err := dst.WriteLog(f, o); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func minimizeLog(ctx context.Context, r *dst.Runner, path string, seed int64, budget int) int {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ops, err := dst.ReadLog(f)
	_ = f.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	o, err := r.Replay(ctx, seed, ops)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if !o.Failed() {
		fmt.Printf("step log %s (%d steps, seed %d) does not fail\n", path, len(ops), seed)
		return 0
	}
	if o, err = r.Minimize(ctx, o, budget); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	o.WriteTrace(os.Stdout)
	if err := writeLog(path+".min", o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	fmt.Printf("minimal step log (%d of %d steps): %s.min\n", len(o.Steps), len(ops), path)
	return 1
}

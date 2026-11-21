package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

const defaultPkgs = "internal/board,internal/auth,internal/capacity,internal/planlint,internal/redact,internal/idempotency,internal/threads,internal/audit"

type options struct {
	root     string
	pkgs     []string
	files    *regexp.Regexp
	sample   int
	seed     uint64
	parallel int
	timeout  time.Duration
	minScore float64
	list     bool
	verbose  bool
	equiv    map[string]bool
}

const defaultEquivalent = "cmd/subspace-mutate/equivalent.txt"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, err := parseOptions(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "subspace-mutate:", err)
		return 2
	}
	ms, skipped, err := mutants(o)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "subspace-mutate:", err)
		return 2
	}
	if o.list {
		for _, m := range ms {
			_, _ = fmt.Fprintln(stdout, m)
		}
		return 0
	}
	if len(ms) == 0 {
		_, _ = fmt.Fprintln(stderr, "subspace-mutate: no mutants")
		return 2
	}
	work, err := os.MkdirTemp("", "subspace-mutate-")
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "subspace-mutate:", err)
		return 2
	}
	defer func() { _ = os.RemoveAll(work) }()
	r := &runner{root: o.root, work: work, parallel: o.parallel, timeout: o.timeout}
	if o.verbose {
		r.progress = stderr
	}
	outs, err := r.run(ctx, ms)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "subspace-mutate:", err)
		return 2
	}
	score := report(stdout, outs)
	_, _ = fmt.Fprintf(stdout, "%d known equivalent mutants skipped\n", skipped)
	if score < o.minScore {
		_, _ = fmt.Fprintf(stderr, "subspace-mutate: score %.1f%% below minimum %.1f%%\n", score, o.minScore)
		return 1
	}
	return 0
}

func parseOptions(args []string, stderr io.Writer) (*options, error) {
	fs := flag.NewFlagSet("subspace-mutate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	o := &options{}
	pkgs := fs.String("pkgs", defaultPkgs, "comma-separated package directories relative to the module root")
	files := fs.String("files", "", "only mutate files whose module-relative path matches this regexp")
	fs.StringVar(&o.root, "root", "", "module root (default: nearest go.mod above the working directory)")
	fs.IntVar(&o.sample, "sample", 0, "run a random sample of N mutants (0 = all)")
	fs.Uint64Var(&o.seed, "seed", 1, "seed for -sample")
	fs.IntVar(&o.parallel, "parallel", max(1, runtime.NumCPU()/4), "mutants tested concurrently")
	fs.DurationVar(&o.timeout, "timeout", time.Minute, "test timeout per mutant")
	fs.Float64Var(&o.minScore, "min-score", 0, "exit 1 when the mutation score (percent) is below this")
	fs.BoolVar(&o.list, "list", false, "list mutants without running tests")
	fs.BoolVar(&o.verbose, "v", false, "print each mutant result")
	equiv := fs.String("equivalent", defaultEquivalent, "file of known equivalent mutants (path<TAB>source line<TAB>mutation per line), relative to the module root")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if o.parallel < 1 || o.sample < 0 || o.timeout <= 0 {
		return nil, errors.New("-parallel and -timeout must be positive, -sample non-negative")
	}
	for _, p := range strings.Split(*pkgs, ",") {
		if p = strings.TrimSpace(p); p != "" {
			o.pkgs = append(o.pkgs, p)
		}
	}
	if len(o.pkgs) == 0 {
		return nil, errors.New("no packages")
	}
	if *files != "" {
		re, err := regexp.Compile(*files)
		if err != nil {
			return nil, err
		}
		o.files = re
	}
	if o.root == "" {
		root, err := moduleRoot()
		if err != nil {
			return nil, err
		}
		o.root = root
	}
	root, err := filepath.Abs(o.root)
	if err != nil {
		return nil, err
	}
	o.root = root
	explicit := false
	fs.Visit(func(f *flag.Flag) { explicit = explicit || f.Name == "equivalent" })
	if o.equiv, err = loadEquivalent(filepath.Join(o.root, *equiv), explicit); err != nil {
		return nil, err
	}
	return o, nil
}

func loadEquivalent(path string, required bool) (map[string]bool, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if errors.Is(err, os.ErrNotExist) && !required {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for i, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			return nil, fmt.Errorf("%s:%d: want path<TAB>source line<TAB>mutation", path, i+1)
		}
		out[parts[0]+"\t"+strings.TrimSpace(parts[1])+"\t"+parts[2]] = true
	}
	return out, nil
}

func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found")
		}
		dir = parent
	}
}

func mutants(o *options) ([]*mutant, int, error) {
	files, err := loadPackages(o.root, o.pkgs, o.files)
	if err != nil {
		return nil, 0, err
	}
	type ref struct{ f, s int }
	var refs []ref
	skipped := 0
	for fi, f := range files {
		for si := range f.sites {
			if o.equiv[f.key(si)] {
				skipped++
				continue
			}
			refs = append(refs, ref{fi, si})
		}
	}
	if o.sample > 0 && o.sample < len(refs) {
		rng := rand.New(rand.NewPCG(o.seed, 0)) //nolint:gosec // reproducible sampling, not security
		rng.Shuffle(len(refs), func(i, j int) { refs[i], refs[j] = refs[j], refs[i] })
		refs = refs[:o.sample]
		sort.Slice(refs, func(i, j int) bool {
			if refs[i].f != refs[j].f {
				return refs[i].f < refs[j].f
			}
			return refs[i].s < refs[j].s
		})
	}
	out := make([]*mutant, 0, len(refs))
	for _, r := range refs {
		m, err := files[r.f].render(r.s)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, m)
	}
	return out, skipped, nil
}

type tally struct{ killed, survived, timedOut, invalid int }

func (t *tally) add(s status) {
	switch s {
	case killed:
		t.killed++
	case survived:
		t.survived++
	case timedOut:
		t.timedOut++
	case invalid:
		t.invalid++
	}
}

func (t tally) score() float64 {
	n := t.killed + t.timedOut + t.survived
	if n == 0 {
		return 100
	}
	return 100 * float64(t.killed+t.timedOut) / float64(n)
}

func (t tally) String() string {
	return fmt.Sprintf("killed %d  timeout %d  survived %d  invalid %d  score %.1f%%", t.killed, t.timedOut, t.survived, t.invalid, t.score())
}

func report(w io.Writer, outs []outcome) float64 {
	var total tally
	byFile := map[string]*tally{}
	var names []string
	for _, o := range outs {
		t := byFile[o.m.Rel]
		if t == nil {
			t = &tally{}
			byFile[o.m.Rel] = t
			names = append(names, o.m.Rel)
		}
		t.add(o.status)
		total.add(o.status)
		if o.status == survived {
			_, _ = fmt.Fprintf(w, "SURVIVED %s\t%s\n", o.m, o.m.Code)
		}
	}
	sort.Strings(names)
	_, _ = fmt.Fprintln(w)
	for _, n := range names {
		_, _ = fmt.Fprintf(w, "%-48s %s\n", n, byFile[n])
	}
	_, _ = fmt.Fprintf(w, "%-48s %s\n", "TOTAL", total)
	return total.score()
}

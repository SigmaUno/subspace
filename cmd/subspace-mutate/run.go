package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type status int

const (
	killed status = iota
	survived
	timedOut
	invalid
)

func (s status) String() string {
	return [...]string{"killed", "survived", "timeout", "invalid"}[s]
}

type outcome struct {
	m      *mutant
	status status
}

type runner struct {
	root     string
	work     string
	parallel int
	timeout  time.Duration
	progress io.Writer
}

func (r *runner) run(ctx context.Context, ms []*mutant) ([]outcome, error) {
	n := min(r.parallel, len(ms))
	copies := make([]string, n)
	for i := range copies {
		copies[i] = filepath.Join(r.work, fmt.Sprintf("w%d", i))
		if err := copyModule(r.root, copies[i]); err != nil {
			return nil, err
		}
	}
	if err := r.baseline(ctx, copies, ms); err != nil {
		return nil, err
	}
	jobs := make(chan int)
	out := make([]outcome, len(ms))
	errc := make(chan error, n)
	var mu sync.Mutex
	done := 0
	var wg sync.WaitGroup
	for _, dir := range copies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				st, err := r.one(ctx, dir, ms[i])
				if err != nil {
					errc <- err
					for range jobs {
					}
					return
				}
				out[i] = outcome{m: ms[i], status: st}
				if r.progress != nil {
					mu.Lock()
					done++
					_, _ = fmt.Fprintf(r.progress, "[%d/%d] %s %s\n", done, len(ms), st, ms[i])
					mu.Unlock()
				}
			}
		}()
	}
	for i := range ms {
		if ctx.Err() != nil {
			break
		}
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	close(errc)
	if err := <-errc; err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *runner) baseline(ctx context.Context, copies []string, ms []*mutant) error {
	seen := map[string]bool{}
	var pkgs []string
	for _, m := range ms {
		if !seen[m.Pkg] {
			seen[m.Pkg] = true
			pkgs = append(pkgs, m.Pkg)
		}
	}
	errs := make([]error, len(pkgs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, len(copies))
	for i, pkg := range pkgs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res, out := r.goTest(ctx, copies[0], pkg)
			if res != survived {
				errs[i] = fmt.Errorf("baseline tests of %s do not pass (%s):\n%s", pkg, res, out)
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (r *runner) one(ctx context.Context, dir string, m *mutant) (status, error) {
	path := filepath.Join(dir, filepath.FromSlash(m.Rel))
	orig, err := os.ReadFile(path) //nolint:gosec // file in the temporary copy
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(path, m.src, 0o600); err != nil { //nolint:gosec // file in the temporary copy
		return 0, err
	}
	st, _ := r.goTest(ctx, dir, m.Pkg)
	if err := os.WriteFile(path, orig, 0o600); err != nil { //nolint:gosec // file in the temporary copy
		return 0, err
	}
	return st, nil
}

func (r *runner) goTest(ctx context.Context, dir, pkg string) (status, string) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout+5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-vet=off", "-failfast", "-timeout", r.timeout.String(), "./"+pkg) //nolint:gosec // package directories chosen by the operator
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=readonly")
	cmd.WaitDelay = 10 * time.Second
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	out := buf.String()
	switch {
	case err == nil:
		return survived, out
	case ctx.Err() != nil, strings.Contains(out, "panic: test timed out"):
		return timedOut, out
	case strings.Contains(out, "[build failed]"), strings.Contains(out, "[setup failed]"):
		return invalid, out
	}
	return killed, out
}

func copyModule(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0o750) //nolint:gosec // inside the temporary copy
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // module file being copied
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()) //nolint:gosec // inside the temporary copy
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

package hillclimb

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

var (
	ErrTimeout     = errors.New("measurement timed out")
	ErrOutputLimit = errors.New("measurement output limit exceeded")
	ErrEscape      = errors.New("worktree contains a symlink that escapes it")
)

var DefaultEnv = []string{"PATH", "LANG", "LC_ALL", "TZ"}

type Limits struct {
	CPUSeconds  int64
	MemoryBytes int64
	OpenFiles   int64
}

type GitRunner struct {
	WorkDir   string
	Repos     map[string]string
	Timeout   time.Duration
	MaxOutput int
	Env       []string
	Limits    Limits
	Git       string
	Shell     string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func RunnerFromEnv() (*GitRunner, error) {
	dir := os.Getenv("SUBSPACE_HILLCLIMB_WORKDIR")
	if dir == "" {
		return nil, nil
	}
	r := &GitRunner{WorkDir: dir, Timeout: 10 * time.Minute, MaxOutput: 1 << 20}
	if v := os.Getenv("SUBSPACE_HILLCLIMB_REPOS"); v != "" {
		if err := json.Unmarshal([]byte(v), &r.Repos); err != nil {
			return nil, fmt.Errorf("SUBSPACE_HILLCLIMB_REPOS: %w", err)
		}
	}
	if v := os.Getenv("SUBSPACE_HILLCLIMB_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("SUBSPACE_HILLCLIMB_TIMEOUT: invalid duration %q", v)
		}
		r.Timeout = d
	}
	ints := []struct {
		name string
		dst  *int64
	}{
		{"SUBSPACE_HILLCLIMB_RLIMIT_CPU", &r.Limits.CPUSeconds},
		{"SUBSPACE_HILLCLIMB_RLIMIT_MEM", &r.Limits.MemoryBytes},
		{"SUBSPACE_HILLCLIMB_RLIMIT_NOFILE", &r.Limits.OpenFiles},
	}
	for _, x := range ints {
		if v := os.Getenv(x.name); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("%s must be a positive integer", x.name)
			}
			*x.dst = n
		}
	}
	if v := os.Getenv("SUBSPACE_HILLCLIMB_MAX_OUTPUT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return nil, errors.New("SUBSPACE_HILLCLIMB_MAX_OUTPUT must be a positive integer")
		}
		r.MaxOutput = n
	}
	for _, name := range strings.Split(os.Getenv("SUBSPACE_HILLCLIMB_ENV"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			r.Env = append(r.Env, name)
		}
	}
	return r, nil
}

var branchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

func validBranch(b string) bool {
	return len(b) <= 200 && branchRe.MatchString(b) && !strings.Contains(b, "..") && !strings.Contains(b, "//") &&
		!strings.Contains(b, "/.") && !strings.HasSuffix(b, "/") && !strings.HasSuffix(b, ".") && !strings.HasSuffix(b, ".lock")
}

func (r *GitRunner) Run(ctx context.Context, repo, branch, cmd string) (string, error) {
	url, ok := r.Repos[repo]
	if !ok {
		return "", model.Forbidden("repo %s is not allowed for hill-climb measurement", repo)
	}
	if !validBranch(branch) {
		return "", model.BadRequest("invalid branch name %q", branch)
	}
	if err := os.MkdirAll(r.WorkDir, 0o750); err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(r.WorkDir)
	if err != nil {
		return "", err
	}
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	id, err := randomID()
	if err != nil {
		return "", err
	}
	run := filepath.Join(root, "runs", id)
	src := filepath.Join(run, "src")
	for _, d := range []string{filepath.Join(run, "home"), filepath.Join(run, "tmp")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return "", err
		}
	}
	defer func() { _ = os.RemoveAll(run) }()
	cache := filepath.Join(root, "cache", repoKey(repo)+".git")
	if err := r.checkout(ctx, cache, url, branch, id, src); err != nil {
		if ctx.Err() != nil {
			return "", ErrTimeout
		}
		return "", err
	}
	defer r.removeWorktree(cache, src)
	if err := confined(src); err != nil {
		return "", err
	}
	return r.exec(ctx, run, src, cmd)
}

func (r *GitRunner) lock(cache string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locks == nil {
		r.locks = map[string]*sync.Mutex{}
	}
	m := r.locks[cache]
	if m == nil {
		m = &sync.Mutex{}
		r.locks[cache] = m
	}
	return m
}

func (r *GitRunner) checkout(ctx context.Context, cache, url, branch, id, src string) error {
	mu := r.lock(cache)
	mu.Lock()
	defer mu.Unlock()
	if _, err := os.Stat(filepath.Join(cache, "HEAD")); err != nil {
		if err := os.MkdirAll(cache, 0o750); err != nil {
			return err
		}
		if _, err := r.git(ctx, cache, "init", "--bare", "--quiet"); err != nil {
			return err
		}
	}
	ref := "refs/hillclimb/" + id
	if _, err := r.git(ctx, cache, "fetch", "--quiet", "--no-tags", url, "+refs/heads/"+branch+":"+ref); err != nil {
		return model.BadRequest("fetch branch %s: %v", branch, err)
	}
	defer func() { _, _ = r.git(context.Background(), cache, "update-ref", "-d", ref) }()
	_, err := r.git(ctx, cache, "worktree", "add", "--quiet", "--detach", src, ref)
	return err
}

func (r *GitRunner) removeWorktree(cache, src string) {
	mu := r.lock(cache)
	mu.Lock()
	defer mu.Unlock()
	_, _ = r.git(context.Background(), cache, "worktree", "remove", "--force", src)
	_ = os.RemoveAll(src)
	_, _ = r.git(context.Background(), cache, "worktree", "prune")
}

func (r *GitRunner) git(ctx context.Context, dir string, args ...string) (string, error) {
	bin := r.Git
	if bin == "" {
		bin = "git"
	}
	c := exec.CommandContext(ctx, bin, append([]string{"-C", dir}, args...)...) //nolint:gosec // git binary is chosen by the operator
	c.Env = append(pick(append([]string{"HOME", "SSH_AUTH_SOCK"}, r.Env...)),
		"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	if err := c.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(tail(out.String())))
	}
	return out.String(), nil
}

func (r *GitRunner) exec(ctx context.Context, run, src, cmd string) (string, error) {
	shell := r.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	out := &capped{max: r.MaxOutput, over: func() { cancel(ErrOutputLimit) }}
	c := exec.CommandContext(ctx, shell, r.shellArgs(shell, cmd)...) //nolint:gosec // running the job command is the purpose of the runner
	c.Dir = src
	c.Env = append(pick(append(append([]string{}, DefaultEnv...), r.Env...)),
		"HOME="+filepath.Join(run, "home"), "TMPDIR="+filepath.Join(run, "tmp"), "PWD="+src)
	c.Stdout, c.Stderr = out, out
	setGroup(c)
	c.Cancel = func() error { return killGroup(c) }
	c.WaitDelay = time.Second
	err := c.Start()
	if err == nil {
		err = c.Wait()
		_ = killGroup(c)
	}
	switch {
	case errors.Is(context.Cause(ctx), ErrOutputLimit):
		return out.String(), ErrOutputLimit
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return out.String(), ErrTimeout
	}
	return out.String(), err
}

func (r *GitRunner) shellArgs(shell, cmd string) []string {
	if runtime.GOOS != "linux" {
		return []string{"-c", cmd}
	}
	var pre []string
	if r.Limits.CPUSeconds > 0 {
		pre = append(pre, "ulimit -t "+strconv.FormatInt(r.Limits.CPUSeconds, 10))
	}
	if r.Limits.MemoryBytes > 0 {
		pre = append(pre, "ulimit -v "+strconv.FormatInt(max(r.Limits.MemoryBytes/1024, 1), 10))
	}
	if r.Limits.OpenFiles > 0 {
		pre = append(pre, "ulimit -n "+strconv.FormatInt(r.Limits.OpenFiles, 10))
	}
	if len(pre) == 0 {
		return []string{"-c", cmd}
	}
	return []string{"-c", strings.Join(pre, " && ") + ` || exit 126; exec "$0" -c "$1"`, shell, cmd}
}

func confined(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		target, err := os.Readlink(p)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(p), target)
		}
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil || !within(root, target) || !within(root, resolved) {
			rp, _ := filepath.Rel(root, p)
			return fmt.Errorf("%w: %s", ErrEscape, rp)
		}
		return nil
	})
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, filepath.Clean(p))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

func pick(names []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		if v, ok := os.LookupEnv(n); ok {
			out = append(out, n+"="+v)
		}
	}
	return out
}

func repoKey(repo string) string {
	h := sha256.Sum256([]byte(repo))
	return hex.EncodeToString(h[:8])
}

func randomID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

type capped struct {
	max  int
	buf  bytes.Buffer
	over func()
	hit  bool
}

func (c *capped) Write(p []byte) (int, error) {
	if c.hit {
		return len(p), nil
	}
	if c.max > 0 && c.buf.Len()+len(p) > c.max {
		c.buf.Write(p[:c.max-c.buf.Len()])
		c.hit = true
		c.over()
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *capped) String() string { return c.buf.String() }

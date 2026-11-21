package mergequeue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	StateQueued  = "queued"
	StateRunning = "running"
	StateWaiting = "waiting"
	StateMerged  = "merged"
	StateEjected = "ejected"
)

const maxLog = 64 << 10

type Candidate struct {
	ID         string    `json:"id"`
	Repo       string    `json:"repo"`
	PR         int       `json:"pr"`
	Head       string    `json:"head"`
	Base       string    `json:"base"`
	TaskID     string    `json:"task_id"`
	State      string    `json:"state"`
	MergedSHA  string    `json:"merged_sha,omitempty"`
	Log        string    `json:"log,omitempty"`
	EnqueuedAt time.Time `json:"enqueued_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (c *Candidate) finished() bool {
	return c.State == StateMerged || c.State == StateEjected
}

type RepoConfig struct {
	Remote string
	Verify []string
	Auto   bool
}

type Runner interface {
	Run(ctx context.Context, dir, name string, args ...string) (string, error)
}

type Notifier interface {
	Notify(ctx context.Context, taskID, body string) error
}

type Gate interface {
	Approved(ctx context.Context, c Candidate) (bool, error)
}

type Tasks interface {
	RequestChanges(ctx context.Context, taskID, reason string) error
}

type Config struct {
	Repos    map[string]RepoConfig
	WorkDir  string
	Store    Store
	Runner   Runner
	Notifier Notifier
	Gate     Gate
	Tasks    Tasks
	Logger   *slog.Logger
	Now      func() time.Time
}

type Queue struct {
	cfg  Config
	mu   sync.Mutex
	busy map[string]bool
}

func New(cfg Config) (*Queue, error) {
	if cfg.WorkDir == "" || cfg.Store == nil || cfg.Notifier == nil || cfg.Gate == nil || cfg.Tasks == nil {
		return nil, errors.New("mergequeue: WorkDir, Store, Notifier, Gate and Tasks are required")
	}
	for name, rc := range cfg.Repos {
		if rc.Remote == "" || len(rc.Verify) == 0 {
			return nil, fmt.Errorf("mergequeue: repo %q needs Remote and Verify", name)
		}
	}
	if cfg.Runner == nil {
		cfg.Runner = ExecRunner{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Queue{cfg: cfg, busy: map[string]bool{}}, nil
}

func (q *Queue) Enqueue(ctx context.Context, c Candidate) (Candidate, error) {
	if _, ok := q.cfg.Repos[c.Repo]; !ok {
		return Candidate{}, model.BadRequest("mergequeue: repo %q is not configured", c.Repo)
	}
	if c.PR <= 0 || c.TaskID == "" {
		return Candidate{}, model.BadRequest("mergequeue: pr and task_id are required")
	}
	if !validBranch(c.Head) || !validBranch(c.Base) || c.Head == c.Base {
		return Candidate{}, model.BadRequest("mergequeue: invalid head %q or base %q", c.Head, c.Base)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	existing, err := q.cfg.Store.List(ctx, c.Repo)
	if err != nil {
		return Candidate{}, err
	}
	for _, e := range existing {
		if e.PR == c.PR && !e.finished() {
			return Candidate{}, model.Conflict("mergequeue: %s#%d is already queued as %s", c.Repo, c.PR, e.ID)
		}
	}
	now := q.cfg.Now()
	c.ID, c.State, c.MergedSHA, c.Log = "", StateQueued, "", ""
	c.EnqueuedAt, c.UpdatedAt = now, now
	if err := q.cfg.Store.Insert(ctx, &c); err != nil {
		return Candidate{}, err
	}
	return c, nil
}

func (q *Queue) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		for repo := range q.cfg.Repos {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for ctx.Err() == nil {
					c, err := q.Process(ctx, repo)
					if err != nil {
						q.cfg.Logger.Warn("mergequeue", "repo", repo, "err", err)
					}
					if c == nil || err != nil {
						return
					}
				}
			}()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (q *Queue) Process(ctx context.Context, repo string) (*Candidate, error) {
	rc, ok := q.cfg.Repos[repo]
	if !ok {
		return nil, model.BadRequest("mergequeue: repo %q is not configured", repo)
	}
	if !q.acquire(repo) {
		return nil, nil
	}
	defer q.release(repo)
	c, err := q.next(ctx, repo)
	if err != nil || c == nil {
		return nil, err
	}
	return c, q.run(ctx, repo, rc, c)
}

func (q *Queue) acquire(repo string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.busy[repo] {
		return false
	}
	q.busy[repo] = true
	return true
}

func (q *Queue) release(repo string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.busy, repo)
}

func (q *Queue) next(ctx context.Context, repo string) (*Candidate, error) {
	list, err := q.cfg.Store.List(ctx, repo)
	if err != nil {
		return nil, err
	}
	for _, c := range list {
		switch c.State {
		case StateQueued, StateRunning:
			return c, nil
		case StateWaiting:
			ok, err := q.cfg.Gate.Approved(ctx, *c)
			if err != nil {
				return nil, err
			}
			if ok {
				return c, nil
			}
		}
	}
	return nil, nil
}

type rejection struct{ reason string }

func (r *rejection) Error() string { return r.reason }

func (q *Queue) run(ctx context.Context, repo string, rc RepoConfig, c *Candidate) error {
	if err := q.save(ctx, c, StateRunning); err != nil {
		return err
	}
	var log strings.Builder
	dir := filepath.Join(q.cfg.WorkDir, dirName(repo))
	headSHA, sha, err := q.prepare(ctx, dir, rc, c, &log)
	if err == nil {
		err = q.verify(ctx, dir, rc, &log)
	}
	approved := rc.Auto
	if err == nil && !approved {
		approved, err = q.cfg.Gate.Approved(ctx, *c)
	}
	c.Log = tail(log.String())
	if err != nil {
		return q.fail(ctx, c, err)
	}
	verify := strings.Join(rc.Verify, " ")
	if !approved {
		return errors.Join(
			q.save(ctx, c, StateWaiting),
			q.notify(ctx, c, fmt.Sprintf("merge queue: PR #%d rebased onto %s at %s, `%s` passed; waiting for owner approval to merge.", c.PR, c.Base, short(sha), verify)),
		)
	}
	_, err = q.git(ctx, dir, &log, "push", "--atomic", "--porcelain",
		"--force-with-lease=refs/heads/"+c.Head+":"+headSHA,
		"origin", sha+":refs/heads/"+c.Base, sha+":refs/heads/"+c.Head)
	c.Log = tail(log.String())
	if err != nil {
		return q.fail(ctx, c, fmt.Errorf("mergequeue: push %s#%d: %w", c.Repo, c.PR, err))
	}
	c.MergedSHA = sha
	return errors.Join(
		q.save(ctx, c, StateMerged),
		q.notify(ctx, c, fmt.Sprintf("merge queue: PR #%d rebased onto %s, `%s` passed, merged (fast-forward) as %s.", c.PR, c.Base, verify, short(sha))),
	)
}

func (q *Queue) prepare(ctx context.Context, dir string, rc RepoConfig, c *Candidate, log *strings.Builder) (headSHA, sha string, err error) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return "", "", err
		}
		if _, err := q.git(ctx, dir, log, "init", "-q"); err != nil {
			return "", "", err
		}
		if _, err := q.git(ctx, dir, log, "remote", "add", "origin", rc.Remote); err != nil {
			return "", "", err
		}
	} else if _, err := q.git(ctx, dir, log, "remote", "set-url", "origin", rc.Remote); err != nil {
		return "", "", err
	}
	base, head := "refs/remotes/origin/"+c.Base, "refs/remotes/origin/"+c.Head
	if _, err := q.git(ctx, dir, log, "fetch", "--no-tags", "--prune", "origin",
		"+refs/heads/"+c.Base+":"+base, "+refs/heads/"+c.Head+":"+head); err != nil {
		return "", "", err
	}
	if headSHA, err = q.git(ctx, dir, log, "rev-parse", "--verify", head+"^{commit}"); err != nil {
		return "", "", err
	}
	if _, err := q.git(ctx, dir, log, "checkout", "-q", "-f", "-B", "mergequeue", head); err != nil {
		return "", "", err
	}
	if _, err := q.git(ctx, dir, log, "clean", "-q", "-fdx"); err != nil {
		return "", "", err
	}
	if _, err := q.git(ctx, dir, log, "-c", "user.name=subspace-mergequeue", "-c", "user.email=mergequeue@subspace.invalid",
		"rebase", base); err != nil {
		_, _ = q.git(ctx, dir, log, "rebase", "--abort")
		return "", "", &rejection{reason: fmt.Sprintf("rebase of %s onto %s failed", c.Head, c.Base)}
	}
	sha, err = q.git(ctx, dir, log, "rev-parse", "--verify", "HEAD^{commit}")
	return headSHA, sha, err
}

func (q *Queue) verify(ctx context.Context, dir string, rc RepoConfig, log *strings.Builder) error {
	fmt.Fprintf(log, "$ %s\n", strings.Join(rc.Verify, " "))
	out, err := q.cfg.Runner.Run(ctx, dir, rc.Verify[0], rc.Verify[1:]...)
	log.WriteString(out)
	if err != nil {
		fmt.Fprintf(log, "%v\n", err)
		return &rejection{reason: fmt.Sprintf("`%s` failed", strings.Join(rc.Verify, " "))}
	}
	return nil
}

func (q *Queue) fail(ctx context.Context, c *Candidate, err error) error {
	var rej *rejection
	if !errors.As(err, &rej) || ctx.Err() != nil {
		return errors.Join(err, q.save(ctx, c, StateQueued))
	}
	ctx = context.WithoutCancel(ctx)
	return errors.Join(
		q.save(ctx, c, StateEjected),
		q.notify(ctx, c, fmt.Sprintf("merge queue: PR #%d ejected: %s.\n\n```\n%s\n```", c.PR, rej.reason, tailN(c.Log, 4000))),
		q.cfg.Tasks.RequestChanges(ctx, c.TaskID, "merge queue: "+rej.reason),
	)
}

func (q *Queue) save(ctx context.Context, c *Candidate, state string) error {
	c.State = state
	c.UpdatedAt = q.cfg.Now()
	return q.cfg.Store.Update(context.WithoutCancel(ctx), c)
}

func (q *Queue) notify(ctx context.Context, c *Candidate, body string) error {
	return q.cfg.Notifier.Notify(context.WithoutCancel(ctx), c.TaskID, body)
}

func (q *Queue) git(ctx context.Context, dir string, log *strings.Builder, args ...string) (string, error) {
	fmt.Fprintf(log, "$ git %s\n", redactCredentials(strings.Join(args, " ")))
	out, err := q.cfg.Runner.Run(ctx, dir, "git", args...)
	log.WriteString(redactCredentials(out))
	if err != nil {
		fmt.Fprintf(log, "%v\n", err)
	}
	return strings.TrimSpace(out), err
}

var (
	unsafeDirChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)
	urlUserinfo    = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/@\s]+@`)
)

func redactCredentials(s string) string {
	return urlUserinfo.ReplaceAllString(s, "${1}[redacted]@")
}

func dirName(repo string) string {
	return unsafeDirChars.ReplaceAllString(repo, "_")
}

func validBranch(b string) bool {
	if b == "" || strings.HasPrefix(b, "-") || strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") ||
		strings.HasSuffix(b, ".") || strings.HasSuffix(b, ".lock") ||
		strings.Contains(b, "..") || strings.Contains(b, "//") || strings.Contains(b, "@{") {
		return false
	}
	for _, r := range b {
		if r <= ' ' || r == 0x7f || strings.ContainsRune(`~^:?*[\`, r) {
			return false
		}
	}
	return true
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func tail(s string) string { return tailN(s, maxLog) }

func tailN(s string, n int) string {
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return strings.ToValidUTF8(s, "")
}

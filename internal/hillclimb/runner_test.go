package hillclimb_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/hillclimb"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/testdb"
)

type fixtureRepo struct {
	t    *testing.T
	bare string
	work string
}

func newRepo(t *testing.T) *fixtureRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	r := &fixtureRepo{t: t, bare: filepath.Join(dir, "origin.git"), work: filepath.Join(dir, "work")}
	r.git(dir, "init", "--bare", "--quiet", r.bare)
	r.git(dir, "init", "--quiet", "-b", "main", r.work)
	return r
}

func (r *fixtureRepo) git(dir string, args ...string) {
	r.t.Helper()
	c := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	if out, err := c.CombinedOutput(); err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (r *fixtureRepo) push(branch string, files map[string]string, links map[string]string) {
	r.t.Helper()
	r.git(r.work, "checkout", "--quiet", "--orphan", "b-"+strings.ReplaceAll(branch, "/", "-"))
	r.git(r.work, "rm", "-rf", "--quiet", "--ignore-unmatch", ".")
	for name, body := range files {
		p := filepath.Join(r.work, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			r.t.Fatal(err)
		}
	}
	for name, target := range links {
		p := filepath.Join(r.work, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			r.t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			r.t.Fatal(err)
		}
	}
	r.git(r.work, "add", "-A")
	r.git(r.work, "commit", "--quiet", "--allow-empty", "-m", branch)
	r.git(r.work, "push", "--quiet", "--force", r.bare, "HEAD:refs/heads/"+branch)
}

func newRunner(t *testing.T, repo *fixtureRepo) *hillclimb.GitRunner {
	t.Helper()
	return &hillclimb.GitRunner{
		WorkDir:   filepath.Join(t.TempDir(), "hc"),
		Repos:     map[string]string{"o/r": repo.bare},
		Timeout:   20 * time.Second,
		MaxOutput: 1 << 16,
	}
}

func TestRunnerFreshWorktreeAndEnvAllowlist(t *testing.T) {
	repo := newRepo(t)
	repo.push("sub/t-1-a", map[string]string{"eval.sh": "echo loss 0.42\n"}, map[string]string{"alias.sh": "eval.sh"})
	repo.push("sub/t-1-a-v2", map[string]string{"eval.sh": "echo loss 0.17\n"}, nil)
	t.Setenv("HC_LEAK_PROBE", "must-not-leak")
	t.Setenv("HC_ALLOWED_PROBE", "visible")
	r := newRunner(t, repo)
	r.Env = []string{"HC_ALLOWED_PROBE"}

	cmd := `test ! -e scratch || echo STALE; touch scratch; sh alias.sh; env; echo cwd=$(pwd -P)`
	out, err := r.Run(context.Background(), "o/r", "sub/t-1-a", cmd)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if v, ok := hillclimb.LastNumber(strings.Split(out, "\n")[0]); !ok || v != 0.42 {
		t.Fatalf("metric line: %q", out)
	}
	if strings.Contains(out, "must-not-leak") || !strings.Contains(out, "HC_ALLOWED_PROBE=visible") {
		t.Fatalf("environment not filtered:\n%s", out)
	}
	root, _ := filepath.EvalSymlinks(r.WorkDir)
	if !strings.Contains(out, "cwd="+filepath.Join(root, "runs")) {
		t.Fatalf("cwd outside workdir:\n%s", out)
	}

	out, err = r.Run(context.Background(), "o/r", "sub/t-1-a", cmd)
	if err != nil || strings.Contains(out, "STALE") {
		t.Fatalf("second run not fresh: %v\n%s", err, out)
	}
	out, err = r.Run(context.Background(), "o/r", "sub/t-1-a-v2", "sh eval.sh")
	if v, _ := hillclimb.LastNumber(out); err != nil || v != 0.17 {
		t.Fatalf("v2: %v %q", err, out)
	}
	if es, _ := os.ReadDir(filepath.Join(root, "runs")); len(es) != 0 {
		t.Fatalf("run dirs left behind: %d", len(es))
	}

	for _, tc := range []struct{ repo, branch, code string }{
		{"o/other", "sub/t-1-a", "forbidden"},
		{"o/r", "../escape", "bad_request"},
		{"o/r", "-upload-pack=x", "bad_request"},
		{"o/r", "sub/missing", "bad_request"},
	} {
		_, err := r.Run(context.Background(), tc.repo, tc.branch, "true")
		if model.AsError(err).Code != tc.code {
			t.Fatalf("%s %s: %v, want %s", tc.repo, tc.branch, err, tc.code)
		}
	}
}

func TestRunnerTimeoutKillsProcessGroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs /proc")
	}
	repo := newRepo(t)
	repo.push("main", map[string]string{"README": "x"}, nil)
	r := newRunner(t, repo)
	r.Timeout = 2 * time.Second

	start := time.Now()
	out, err := r.Run(context.Background(), "o/r", "main", `sleep 300 & echo child $!; sleep 300`)
	if !errors.Is(err, hillclimb.ErrTimeout) {
		t.Fatalf("err = %v, out %q", err, out)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("timeout took %v", d)
	}
	var pid int
	if _, err := fmt.Sscanf(out, "child %d", &pid); err != nil || pid <= 0 {
		t.Fatalf("no child pid in %q", out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("background child %d survived the timeout", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func alive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	f := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+1:]))
	return len(f) > 0 && f[0] != "Z" && f[0] != "X"
}

func TestRunnerOutputCap(t *testing.T) {
	repo := newRepo(t)
	repo.push("main", map[string]string{"README": "x"}, nil)
	r := newRunner(t, repo)
	r.MaxOutput = 1000

	start := time.Now()
	out, err := r.Run(context.Background(), "o/r", "main", `yes 0.5`)
	if !errors.Is(err, hillclimb.ErrOutputLimit) || len(out) != 1000 {
		t.Fatalf("err = %v, len(out) = %d", err, len(out))
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("output cap took %v", d)
	}
}

func TestRunnerRejectsSymlinkEscape(t *testing.T) {
	cases := map[string]map[string]string{
		"absolute": {"data": "/etc"},
		"relative": {"deep/data": "../../../../../../../../etc/passwd"},
		"chained":  {"d/up": "..", "e": "d/up/../home"},
		"dangling": {"later": "/nonexistent-hillclimb-target"},
	}
	for name, links := range cases {
		t.Run(name, func(t *testing.T) {
			repo := newRepo(t)
			repo.push("main", map[string]string{"README": "x"}, links)
			r := newRunner(t, repo)
			marker := filepath.Join(t.TempDir(), "ran")
			_, err := r.Run(context.Background(), "o/r", "main", "touch "+marker)
			if !errors.Is(err, hillclimb.ErrEscape) {
				t.Fatalf("err = %v", err)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatal("command ran despite symlink escape")
			}
		})
	}
}

func TestRunnerCPULimit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("rlimits are applied on linux only")
	}
	repo := newRepo(t)
	repo.push("main", map[string]string{"README": "x"}, nil)
	r := newRunner(t, repo)
	r.Limits.CPUSeconds = 1

	start := time.Now()
	_, err := r.Run(context.Background(), "o/r", "main", `while :; do :; done`)
	if err == nil || errors.Is(err, hillclimb.ErrTimeout) {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Fatalf("cpu limit took %v", d)
	}
}

func TestSQLStoreSurvivesRestart(t *testing.T) {
	path := testdb.Path(t, "hc.db")
	open := func(outs ...string) (store.Store, *hillclimb.Measurer) {
		st := testdb.Open(t, path)
		m := hillclimb.NewMeasurer(hillclimb.NewSQLStore(st), &fakeRunner{outs: outs})
		c := &clock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
		m.Now = c.now
		return st, m
	}
	st, m := open("loss 0.9", "loss 0.4")
	target := 0.1
	job := &hillclimb.Job{BriefID: "B-1", Repo: "o/r", MetricCmd: "sh eval.sh", Direction: "min", Target: &target, Budget: 3}
	if err := m.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"sub/a", "sub/b"} {
		if _, _, err := m.Measure(context.Background(), job.ID, "w1", b); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, m = open("loss 0.05")
	defer func() { _ = st.Close() }()
	j, err := m.Store.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != hillclimb.JobRunning || j.Attempts != 2 || *j.Best != 0.4 || *j.Target != 0.1 || j.Budget != 3 || j.MetricCmd != "sh eval.sh" {
		t.Fatalf("job after restart: %+v", j)
	}
	as, err := m.Store.ListAttempts(job.ID)
	if err != nil || len(as) != 2 || as[1].Branch != "sub/b" || *as[0].Value != 0.9 || *j.BestAttemptID != as[1].ID {
		t.Fatalf("attempts after restart: %v %+v", err, as)
	}
	_, j, err = m.Measure(context.Background(), job.ID, "w1", "sub/c")
	if err != nil || j.Status != hillclimb.JobStopped || *j.StopReason != hillclimb.StopTarget || j.Attempts != 3 {
		t.Fatalf("measure after restart: %v %+v", err, j)
	}
	if js, _ := m.Store.ListJobs(); len(js) != 1 || js[0].ID != job.ID {
		t.Fatalf("jobs: %+v", js)
	}
	if _, err := m.Store.GetJob("H-99"); model.AsError(err).Code != "not_found" {
		t.Fatalf("missing job: %v", err)
	}
}

package acceptance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type gitRepo struct {
	mu     sync.Mutex
	remote string
	seed   string
}

func newGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "scripted")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "scripted@example.invalid")
	}
	root := t.TempDir()
	g := &gitRepo{remote: filepath.Join(root, "remote.git"), seed: filepath.Join(root, "seed")}
	for _, step := range []struct {
		dir  string
		args []string
	}{
		{root, []string{"init", "-q", "--bare", "-b", "main", g.remote}},
		{root, []string{"clone", "-q", g.remote, g.seed}},
		{g.seed, []string{"symbolic-ref", "HEAD", "refs/heads/main"}},
	} {
		if _, err := g.git(step.dir, step.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.commit("README", "shop\n", "initial"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.git(g.seed, "push", "-q", "origin", "main"); err != nil {
		t.Fatal(err)
	}
	return g
}

func (g *gitRepo) git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %v: %w\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

func (g *gitRepo) commit(file, content, msg string) error {
	if err := os.WriteFile(filepath.Join(g.seed, file), []byte(content), 0o600); err != nil {
		return err
	}
	if _, err := g.git(g.seed, "add", file); err != nil {
		return err
	}
	_, err := g.git(g.seed, "commit", "-q", "-m", msg)
	return err
}

func (g *gitRepo) push(branch, file, content string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, err := g.git(g.seed, "fetch", "-q", "origin"); err != nil {
		return err
	}
	if _, err := g.git(g.seed, "checkout", "-q", "-B", branch, "origin/main"); err != nil {
		return err
	}
	if err := g.commit(file, content, "add "+file); err != nil {
		return err
	}
	_, err := g.git(g.seed, "push", "-q", "-f", "origin", branch)
	return err
}

func (g *gitRepo) files(ref string) ([]string, error) {
	out, err := g.git(g.remote, "ls-tree", "-r", "--name-only", ref)
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

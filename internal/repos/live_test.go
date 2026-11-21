package repos_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/forgejotest"
	"github.com/SigmaUno/subspace/internal/repos"
)

func TestLiveAgentEnvPushesBranchAndOpensPR(t *testing.T) {
	inst := forgejotest.Start(t)
	hosted := inst.NewRepo(t)
	t.Setenv("LIVE_AGENT_TOKEN", inst.Token)
	r := repos.Repo{Name: hosted.FullName(), Host: repos.HostForgejo, BaseURL: inst.URL, DefaultBranch: "main", AgentTokenEnv: "LIVE_AGENT_TOKEN"}
	granted, _ := r.AgentEnv()
	if granted == nil {
		t.Fatal("no agent environment")
	}

	home := t.TempDir()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=agent", "GIT_AUTHOR_EMAIL=agent@example.invalid", "GIT_COMMITTER_NAME=agent", "GIT_COMMITTER_EMAIL=agent@example.invalid"}
	for k, v := range granted {
		env = append(env, k+"="+v)
	}
	work := filepath.Join(home, "work")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...) //nolint:gosec // test runs git with fixed arguments
		cmd.Dir, cmd.Env = dir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, strings.ReplaceAll(string(out), inst.Token, "[token]"))
		}
	}
	u, err := url.Parse(inst.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + hosted.FullName() + ".git"
	if u.User != nil {
		t.Fatal("clone URL carries credentials")
	}
	git(home, "clone", "-q", u.String(), work)
	const branch = "sub/t-1-agent-push"
	git(work, "checkout", "-q", "-b", branch)
	if err := os.WriteFile(filepath.Join(work, "agent.txt"), []byte("pushed with the spawner environment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(work, "add", "agent.txt")
	git(work, "commit", "-q", "-m", "T-1: add agent.txt")
	git(work, "push", "-q", "origin", branch)

	body, _ := json.Marshal(map[string]string{"head": branch, "base": granted["SUBSPACE_GIT_DEFAULT_BRANCH"], "title": "T-1: add agent.txt"})
	req, err := http.NewRequest(http.MethodPost, granted["GITEA_SERVER_URL"]+"/api/v1/repos/"+hosted.FullName()+"/pulls", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "token "+granted["GITEA_SERVER_TOKEN"])
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var pr forgejotest.PR
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("open pull request: %d %v", resp.StatusCode, err)
	}
	if got := hosted.GetPR(t, pr.Number); got.Head.Ref != branch || got.State != "open" {
		t.Fatalf("pull request %+v", got)
	}
}

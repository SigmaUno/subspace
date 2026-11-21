package forgejotest

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const Enable = "SUBSPACE_TEST_FORGEJO"

type Instance struct {
	URL       string
	Token     string
	Admin     string
	Container string

	http *http.Client
}

func Start(t testing.TB) *Instance {
	t.Helper()
	if os.Getenv(Enable) != "1" {
		t.Skip(Enable + "=1 is required")
	}
	i := &Instance{
		URL:       os.Getenv("SUBSPACE_FORGEJO_URL"),
		Token:     os.Getenv("SUBSPACE_FORGEJO_TOKEN"),
		Admin:     os.Getenv("SUBSPACE_FORGEJO_ADMIN"),
		Container: os.Getenv("SUBSPACE_FORGEJO_CONTAINER"),
		http:      &http.Client{Timeout: 30 * time.Second},
	}
	if i.URL != "" && i.Token != "" && i.Admin != "" {
		return i
	}
	i.launch(t)
	return i
}

func (i *Instance) launch(t testing.TB) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	script := filepath.Join(filepath.Dir(file), "..", "..", "scripts", "forgejo-live.sh")
	out, err := exec.Command(script, "up").Output() //nolint:gosec // repository script
	if err != nil {
		t.Fatalf("start forgejo: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			continue
		}
		switch k {
		case "SUBSPACE_FORGEJO_URL":
			i.URL = v
		case "SUBSPACE_FORGEJO_TOKEN":
			i.Token = v
		case "SUBSPACE_FORGEJO_ADMIN":
			i.Admin = v
		case "SUBSPACE_FORGEJO_CONTAINER":
			i.Container = v
		}
	}
	t.Cleanup(func() { _ = exec.Command(script, "down", i.Container).Run() }) //nolint:gosec // repository script
}

func (i *Instance) Host() string {
	u, _ := url.Parse(i.URL)
	return u.Host
}

func (i *Instance) APIURL() string { return i.URL + "/api/v1" }

type Call struct {
	Status int
	Header http.Header
	Body   []byte
}

func (i *Instance) Raw(t testing.TB, sudo, method, path string, in any) Call {
	t.Helper()
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, i.APIURL()+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "token "+i.Token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if sudo != "" {
		req.Header.Set("Sudo", sudo)
	}
	resp, err := i.http.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return Call{Status: resp.StatusCode, Header: resp.Header, Body: raw}
}

func (i *Instance) Do(t testing.TB, sudo, method, path string, in, out any) {
	t.Helper()
	c := i.Raw(t, sudo, method, path, in)
	if c.Status < 200 || c.Status > 299 {
		t.Fatalf("%s %s: status %d: %s", method, path, c.Status, c.Body)
	}
	if out != nil {
		if err := json.Unmarshal(c.Body, out); err != nil {
			t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
}

func Random(prefix string) string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func (i *Instance) NewUser(t testing.TB) string {
	t.Helper()
	name := Random("u")
	pw := Random("Pw-") + "-Aa1"
	i.Do(t, "", http.MethodPost, "/admin/users", map[string]any{
		"username": name, "email": name + "@example.invalid", "password": pw, "must_change_password": false,
	}, nil)
	return name
}

type Repo struct {
	I     *Instance
	Owner string
	Name  string
}

func (i *Instance) NewRepo(t testing.TB) *Repo {
	t.Helper()
	name := Random("r")
	i.Do(t, "", http.MethodPost, "/user/repos", map[string]any{
		"name": name, "auto_init": true, "default_branch": "main", "private": true,
	}, nil)
	return &Repo{I: i, Owner: i.Admin, Name: name}
}

func (r *Repo) FullName() string { return r.Owner + "/" + r.Name }

func (r *Repo) path(suffix string) string {
	return "/repos/" + url.PathEscape(r.Owner) + "/" + url.PathEscape(r.Name) + suffix
}

func (r *Repo) Path(suffix string) string { return r.path(suffix) }

func (r *Repo) CloneURL() string {
	u, _ := url.Parse(r.I.URL)
	u.User = url.UserPassword(r.I.Admin, r.I.Token)
	u.Path = "/" + r.FullName() + ".git"
	return u.String()
}

func (r *Repo) PRURL(n int64) string {
	return fmt.Sprintf("%s/%s/pulls/%d", r.I.URL, r.FullName(), n)
}

func (r *Repo) Collaborator(t testing.TB, user string) {
	t.Helper()
	r.I.Do(t, "", http.MethodPut, r.path("/collaborators/"+url.PathEscape(user)), map[string]any{"permission": "write"}, nil)
}

type Commit struct {
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

func (r *Repo) hasBranch(t testing.TB, branch string) bool {
	t.Helper()
	return r.I.Raw(t, "", http.MethodGet, r.path("/branches/"+url.PathEscape(branch)), nil).Status == http.StatusOK
}

func (r *Repo) Commit(t testing.TB, branch, base, file, content string) string {
	t.Helper()
	body := map[string]any{
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
		"message": "add " + file,
		"branch":  branch,
	}
	if !r.hasBranch(t, branch) {
		body["branch"], body["new_branch"] = base, branch
	}
	var out Commit
	r.I.Do(t, "", http.MethodPost, r.path("/contents/"+file), body, &out)
	return out.Commit.SHA
}

type PR struct {
	Number  int64  `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"`
	Head    struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
}

func (r *Repo) OpenPR(t testing.TB, head, base, title string) PR {
	t.Helper()
	var pr PR
	r.I.Do(t, "", http.MethodPost, r.path("/pulls"), map[string]any{"head": head, "base": base, "title": title}, &pr)
	return pr
}

func (r *Repo) GetPR(t testing.TB, n int64) PR {
	t.Helper()
	var pr PR
	r.I.Do(t, "", http.MethodGet, r.path(fmt.Sprintf("/pulls/%d", n)), nil, &pr)
	return pr
}

func (r *Repo) Review(t testing.TB, user string, n int64, event string) {
	t.Helper()
	r.I.Do(t, user, http.MethodPost, r.path(fmt.Sprintf("/pulls/%d/reviews", n)), map[string]any{"event": event, "body": event}, nil)
}

func (r *Repo) AddHook(t testing.TB, target, secret string, events ...string) {
	t.Helper()
	r.I.Do(t, "", http.MethodPost, r.path("/hooks"), map[string]any{
		"type": "forgejo", "active": true, "events": events,
		"config": map[string]string{"url": target, "content_type": "json", "secret": secret},
	}, nil)
}

func (r *Repo) WaitMergeable(t testing.TB, n int64) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		c := r.I.Raw(t, "", http.MethodGet, r.path(fmt.Sprintf("/pulls/%d", n)), nil)
		var pr struct {
			Mergeable bool `json:"mergeable"`
		}
		if c.Status == http.StatusOK && json.Unmarshal(c.Body, &pr) == nil && pr.Mergeable {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pull request %d never became mergeable", n)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (r *Repo) Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=live", "GIT_AUTHOR_EMAIL=live@example.invalid",
		"GIT_COMMITTER_NAME=live", "GIT_COMMITTER_EMAIL=live@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	text := strings.ReplaceAll(string(out), r.I.Token, "[token]")
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.ReplaceAll(strings.Join(args, " "), r.I.Token, "[token]"), err, text)
	}
	return strings.TrimSpace(text)
}

func (r *Repo) Clone(t testing.TB) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "work")
	r.Git(t, t.TempDir(), "clone", "-q", r.CloneURL(), dir)
	return dir
}

func (r *Repo) CommitFile(t testing.TB, dir, file, content string) string {
	t.Helper()
	path := filepath.Join(dir, file)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	r.Git(t, dir, "add", file)
	r.Git(t, dir, "commit", "-q", "-m", "add "+file)
	return r.Git(t, dir, "rev-parse", "HEAD")
}

func (r *Repo) AllowManualMerge(t testing.TB) {
	t.Helper()
	r.I.Do(t, "", http.MethodPatch, r.path(""), map[string]any{"allow_manual_merge": true}, nil)
}

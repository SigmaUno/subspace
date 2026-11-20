package spawner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type execCall struct {
	stdin string
	name  string
	args  []string
}

func fakeExec(c *execCall, err error) Exec {
	return func(_ context.Context, stdin []byte, name string, args ...string) error {
		*c = execCall{stdin: string(stdin), name: name, args: args}
		return err
	}
}

func openCodeRequest() Request {
	r := testRequest()
	r.Kind = "opencode-deepseek"
	r.Provider = "deepseek"
	r.Model = "deepseek-chat"
	r.Env = map[string]string{"EXTRA": "it's"}
	return r
}

func TestOpenCodeLocalCommand(t *testing.T) {
	var c execCall
	o := &OpenCode{Root: "/srv/agents", Exec: fakeExec(&c, nil)}
	r := openCodeRequest()
	if err := o.Spawn(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if c.name != "sh" {
		t.Fatalf("name %q", c.name)
	}
	want := []string{
		"-c", openCodeScript, "sh",
		"/srv/agents/.repos/SigmaUno/foo", "/srv/agents/claude-b1-1", "swarm", "claude-b1-1",
		"opencode", "deepseek/deepseek-chat", r.JoinLine,
	}
	if !reflect.DeepEqual(c.args, want) {
		t.Fatalf("args %q", c.args)
	}
	wantEnv := "EXTRA='it'\\''s'\nSUBSPACE_TOKEN='" + testToken + "'\nSUBSPACE_URL='https://s.example.com/mcp'\n"
	if c.stdin != wantEnv {
		t.Fatalf("stdin %q", c.stdin)
	}
	if strings.Contains(strings.Join(c.args, "\x00"), testToken) {
		t.Fatal("token in argv")
	}
}

func TestOpenCodeSSHCommand(t *testing.T) {
	var c execCall
	o := &OpenCode{
		Host: "builder@box", Root: "/srv/agents", ReposDir: "/srv/git", Session: "crew", Bin: "/opt/oc/opencode",
		Exec: fakeExec(&c, nil),
	}
	r := openCodeRequest()
	r.Kind, r.Provider, r.Model = "opencode-anthropic", "anthropic", "anthropic/claude-sonnet-5-5"
	r.JoinLine = "Join it's"
	if err := o.Spawn(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if c.name != "ssh" || len(c.args) != 5 {
		t.Fatalf("%s %q", c.name, c.args)
	}
	if !reflect.DeepEqual(c.args[:4], []string{"-o", "BatchMode=yes", "--", "builder@box"}) {
		t.Fatalf("args %q", c.args)
	}
	remote := c.args[4]
	for _, want := range []string{
		"'sh' '-c' 'set -eu\n", "' 'sh' '/srv/git/SigmaUno/foo' '/srv/agents/claude-b1-1' 'crew' 'claude-b1-1' '/opt/oc/opencode'",
		"'anthropic/claude-sonnet-5-5' 'Join it'\\''s'",
	} {
		if !strings.Contains(remote, want) {
			t.Fatalf("remote missing %q:\n%s", want, remote)
		}
	}
	if strings.Contains(remote, testToken) || strings.Contains(strings.Join(c.args, " "), testToken) {
		t.Fatal("token in argv")
	}
	if !strings.Contains(c.stdin, "SUBSPACE_TOKEN='"+testToken+"'") {
		t.Fatalf("stdin %q", c.stdin)
	}
}

func TestOpenCodeWithoutRepoUsesPlainDirectory(t *testing.T) {
	var c execCall
	o := &OpenCode{Root: "/srv/agents", Exec: fakeExec(&c, nil)}
	r := openCodeRequest()
	r.Repo = ""
	if err := o.Spawn(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if c.args[3] != "" || c.args[4] != "/srv/agents/claude-b1-1" {
		t.Fatalf("args %q", c.args)
	}
}

func TestOpenCodeRejectsBadInput(t *testing.T) {
	cases := map[string]func(*OpenCode, *Request){
		"agent id":      func(_ *OpenCode, r *Request) { r.AgentID = "x; rm -rf /" },
		"kind":          func(_ *OpenCode, r *Request) { r.Kind = "claude-code" },
		"provider":      func(_ *OpenCode, r *Request) { r.Provider = "anthropic" },
		"model":         func(_ *OpenCode, r *Request) { r.Model = "x; reboot" },
		"repo":          func(_ *OpenCode, r *Request) { r.Repo = "../../etc" },
		"env key":       func(_ *OpenCode, r *Request) { r.Env = map[string]string{"BAD KEY": "1"} },
		"empty join":    func(_ *OpenCode, r *Request) { r.JoinLine = " " },
		"host":          func(o *OpenCode, _ *Request) { o.Host = "-oProxyCommand=x" },
		"relative root": func(o *OpenCode, _ *Request) { o.Root = "agents" },
		"session":       func(o *OpenCode, _ *Request) { o.Session = "a:b" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var c execCall
			o := &OpenCode{Root: "/srv/agents", Exec: fakeExec(&c, nil)}
			r := openCodeRequest()
			mutate(o, &r)
			if err := o.Spawn(context.Background(), r); err == nil {
				t.Fatal("expected error")
			}
			if c.name != "" {
				t.Fatal("runner must not be called")
			}
		})
	}
}

func TestOpenCodeRunnerErrorRedacted(t *testing.T) {
	var c execCall
	o := &OpenCode{Root: "/srv/agents", Exec: fakeExec(&c, errors.New("failed with "+testToken))}
	err := o.Spawn(context.Background(), openCodeRequest())
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("err %v", err)
	}
}

func TestNewOpenCode(t *testing.T) {
	env := map[string]string{
		"SUBSPACE_OPENCODE_ROOT": "/srv/a", "SUBSPACE_OPENCODE_HOST": "h", "SUBSPACE_OPENCODE_REPOS": "/srv/r",
		"SUBSPACE_OPENCODE_SESSION": "crew", "SUBSPACE_OPENCODE_BIN": "/x/oc",
	}
	s, err := New("opencode", func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	want := &OpenCode{Host: "h", Root: "/srv/a", ReposDir: "/srv/r", Session: "crew", Bin: "/x/oc"}
	if !reflect.DeepEqual(s, want) || s.Name() != "opencode" {
		t.Fatalf("%#v", s)
	}
	if _, err := New("opencode", nil); err == nil {
		t.Fatal("expected error without root")
	}
}

const fakeTmux = `#!/bin/sh
echo "$@" >>"$FAKE_TMUX_LOG"
case "$1" in
has-session) exit 1 ;;
esac
while [ "$1" != "--" ]; do shift; done
shift
cd "$FAKE_TMUX_CWD" 2>/dev/null || true
"$@"
`

const fakeOpenCode = `#!/bin/sh
{
	echo "args=$*"
	echo "token=$SUBSPACE_TOKEN"
	echo "url=$SUBSPACE_URL"
	echo "extra=$EXTRA"
	echo "pwd=$(pwd)"
} >"$FAKE_OC_OUT"
`

func TestOpenCodeScriptEndToEnd(t *testing.T) {
	for _, bin := range []string{"git", "sh"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}
	tmp := t.TempDir()
	bindir := filepath.Join(tmp, "bin")
	root := filepath.Join(tmp, "agents")
	repos := filepath.Join(tmp, "repos")
	base := filepath.Join(repos, "SigmaUno", "foo")
	for _, d := range []string{bindir, root, base} {
		if err := os.MkdirAll(d, 0o755); err != nil { //nolint:gosec
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"tmux": fakeTmux, "oc": fakeOpenCode} {
		if err := os.WriteFile(filepath.Join(bindir, name), []byte(body), 0o755); err != nil { //nolint:gosec
			t.Fatal(err)
		}
	}
	gitEnv := []string{"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"}
	for _, args := range [][]string{{"init", "-q"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...) //nolint:gosec
		cmd.Dir = base
		cmd.Env = append(os.Environ(), gitEnv...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	envDir := filepath.Join(tmp, "tmp")
	if err := os.Mkdir(envDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ocOut := filepath.Join(tmp, "oc.out")
	wt := filepath.Join(root, "claude-b1-1")
	t.Setenv("PATH", bindir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMPDIR", envDir)
	t.Setenv("FAKE_TMUX_LOG", filepath.Join(tmp, "tmux.log"))
	t.Setenv("FAKE_TMUX_CWD", wt)
	t.Setenv("FAKE_OC_OUT", ocOut)

	o := &OpenCode{Root: root, ReposDir: repos, Bin: filepath.Join(bindir, "oc")}
	r := openCodeRequest()
	if err := o.Spawn(context.Background(), r); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(ocOut) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"args=run --auto -m deepseek/deepseek-chat " + r.JoinLine,
		"token=" + testToken, "url=https://s.example.com/mcp", "extra=it's", "pwd=" + wt,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if _, err := os.Stat(filepath.Join(wt, ".git")); err != nil {
		t.Errorf("worktree not created: %v", err)
	}
	left, err := os.ReadDir(envDir)
	if err != nil || len(left) != 0 {
		t.Errorf("env file left behind: %v %v", left, err)
	}
	logged, err := os.ReadFile(filepath.Join(tmp, "tmux.log")) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logged), testToken) {
		t.Error("token in tmux argv")
	}
	if !strings.Contains(string(logged), "new-session -d -s swarm -n claude-b1-1 -c "+wt) {
		t.Errorf("tmux log: %s", logged)
	}
}

func TestOpenCodeScriptCleansEnvFileOnFailure(t *testing.T) {
	tmp := t.TempDir()
	envDir := filepath.Join(tmp, "tmp")
	if err := os.Mkdir(envDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", envDir)
	o := &OpenCode{Root: filepath.Join(tmp, "agents"), ReposDir: filepath.Join(tmp, "missing")}
	r := openCodeRequest()
	if err := o.Spawn(context.Background(), r); err == nil {
		t.Fatal("expected error")
	}
	left, err := os.ReadDir(envDir)
	if err != nil || len(left) != 0 {
		t.Fatalf("env file left behind: %v %v", left, err)
	}
}

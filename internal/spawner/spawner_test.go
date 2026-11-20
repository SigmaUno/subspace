package spawner

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testToken = "ssp_supersecrettoken"

func testRequest() Request {
	return Request{
		AgentID: "claude-b1-1", Kind: "claude-code", Model: "claude sonnet", Provider: "anthropic",
		Repo: "SigmaUno/foo", JoinLine: JoinLine("https://s.example.com", "claude-b1-1"),
		MCPURL: "https://s.example.com/mcp", Token: testToken,
		Env: map[string]string{"EXTRA": "1"},
	}
}

type call struct {
	name string
	args []string
	env  []string
}

func fakeRunner(c *call, err error) Runner {
	return func(ctx context.Context, name string, args ...string) error {
		*c = call{name: name, args: args, env: EnvFromContext(ctx)}
		return err
	}
}

func TestJoinLine(t *testing.T) {
	got := JoinLine("https://s.example.com/", "claude-1")
	want := "Join Subspace at https://s.example.com as claude-1. Call get_instructions and follow them."
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestSnippets(t *testing.T) {
	cc, codex, oc := Snippets("https://s.example.com", "")
	if want := `claude mcp add --transport http subspace https://s.example.com/mcp --header "Authorization: Bearer <token>"`; cc != want {
		t.Errorf("claude: %q", cc)
	}
	if want := "[mcp_servers.subspace]\nurl = \"https://s.example.com/mcp\"\nhttp_headers = { Authorization = \"Bearer <token>\" }\n"; codex != want {
		t.Errorf("codex: %q", codex)
	}
	if want := `{"mcp":{"subspace":{"type":"remote","url":"https://s.example.com/mcp","headers":{"Authorization":"Bearer <token>"}}}}`; oc != want {
		t.Errorf("opencode: %q", oc)
	}

	cc, codex, oc = Snippets("https://s.example.com", "ssp_abc")
	for _, s := range []string{cc, codex, oc} {
		if !strings.Contains(s, "Bearer ssp_abc") || strings.Contains(s, "<token>") {
			t.Errorf("token not substituted: %q", s)
		}
	}
}

func TestNoopRedacts(t *testing.T) {
	var buf bytes.Buffer
	n := Noop{Log: slog.New(slog.NewTextHandler(&buf, nil))}
	if err := n.Spawn(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, testToken) || strings.Contains(out, "EXTRA") {
		t.Fatalf("secret leaked: %s", out)
	}
	if !strings.Contains(out, "claude-b1-1") {
		t.Fatalf("missing agent id: %s", out)
	}
}

func TestHCLocal(t *testing.T) {
	var c call
	h := &HC{Runner: fakeRunner(&c, nil)}
	if err := h.Spawn(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}
	if c.name != "hc" {
		t.Fatalf("name %q", c.name)
	}
	want := []string{"run", "--bg", "--name", "claude-b1-1", "--", "claude-code", testRequest().JoinLine}
	if !reflect.DeepEqual(c.args, want) {
		t.Fatalf("args %q", c.args)
	}
	if strings.Contains(strings.Join(c.args, " "), testToken) {
		t.Fatal("token in argv")
	}
	wantEnv := []string{"EXTRA=1", "SUBSPACE_TOKEN=" + testToken, "SUBSPACE_URL=https://s.example.com/mcp"}
	if !reflect.DeepEqual(c.env, wantEnv) {
		t.Fatalf("env %q", c.env)
	}
}

func TestHCTemplateKeepsValuesAsOneArg(t *testing.T) {
	var c call
	h := &HC{Bin: "/opt/hc", CommandTmpl: "--model={model} {kind} {join_line}", Runner: fakeRunner(&c, nil)}
	if err := h.Spawn(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}
	want := []string{"run", "--bg", "--model=claude sonnet", "claude-code", testRequest().JoinLine}
	if c.name != "/opt/hc" || !reflect.DeepEqual(c.args, want) {
		t.Fatalf("%s %q", c.name, c.args)
	}
}

func TestHCSSH(t *testing.T) {
	var c call
	h := &HC{Host: "builder@box", Runner: fakeRunner(&c, nil)}
	r := testRequest()
	r.Model = "it's"
	if err := h.Spawn(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if c.name != "ssh" {
		t.Fatalf("name %q", c.name)
	}
	joined := strings.Join(c.args, "\x00")
	if strings.Contains(joined, testToken) {
		t.Fatal("token in argv")
	}
	n := len(c.args)
	if c.args[n-2] != "builder@box" || c.args[n-3] != "--" {
		t.Fatalf("args %q", c.args)
	}
	remote := c.args[n-1]
	if !strings.HasPrefix(remote, "'hc' 'run' '--bg' '--name' 'claude-b1-1'") {
		t.Fatalf("remote %q", remote)
	}
	if !strings.Contains(joined, "SendEnv=SUBSPACE_TOKEN") || !strings.Contains(joined, "SendEnv=SUBSPACE_URL") {
		t.Fatalf("SendEnv missing: %q", c.args)
	}
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Fatalf("quote %q", got)
	}
}

func TestHCRejectsBadInput(t *testing.T) {
	var c call
	h := &HC{Runner: fakeRunner(&c, nil)}
	r := testRequest()
	r.AgentID = "x; rm -rf /"
	if err := h.Spawn(context.Background(), r); err == nil {
		t.Fatal("expected invalid agent id error")
	}
	r = testRequest()
	r.Env = map[string]string{"BAD KEY": "1"}
	if err := h.Spawn(context.Background(), r); err == nil {
		t.Fatal("expected invalid env key error")
	}
	h = &HC{Host: "-oProxyCommand=x", Runner: fakeRunner(&c, nil)}
	if err := h.Spawn(context.Background(), testRequest()); err == nil {
		t.Fatal("expected invalid host error")
	}
	if c.name != "" {
		t.Fatal("runner must not be called")
	}
}

func TestHCRunnerErrorRedacted(t *testing.T) {
	var c call
	boom := errors.New("failed with " + testToken)
	h := &HC{Runner: fakeRunner(&c, boom)}
	err := h.Spawn(context.Background(), testRequest())
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("err %v", err)
	}
}

func TestDocker(t *testing.T) {
	d := Docker{}
	if err := d.Spawn(context.Background(), testRequest()); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("err %v", err)
	}
	if d.Name() != "docker" {
		t.Fatal(d.Name())
	}
}

func TestNew(t *testing.T) {
	for _, n := range []string{"", "noop", "log"} {
		s, err := New(n, nil)
		if err != nil || s.Name() != "noop" {
			t.Fatalf("%q: %v %v", n, s, err)
		}
	}
	env := map[string]string{"SUBSPACE_HC_BIN": "/x/hc", "SUBSPACE_HC_HOST": "h", "SUBSPACE_HC_COMMAND": "{kind}"}
	s, err := New("hc", func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	hc, ok := s.(*HC)
	if !ok || hc.Bin != "/x/hc" || hc.Host != "h" || hc.CommandTmpl != "{kind}" || s.Name() != "hc" {
		t.Fatalf("%#v", s)
	}
	s, err = New("hc", func(string) string { return "" })
	if err != nil || s.(*HC).Bin != "hc" {
		t.Fatalf("%v %v", s, err)
	}
	if s, err = New("docker", nil); err != nil || s.Name() != "docker" {
		t.Fatalf("%v %v", s, err)
	}
	if _, err := New("bogus", nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestExecRunnerEnvAndRedaction(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "fakehc")
	script := "#!/bin/sh\necho \"token=$SUBSPACE_TOKEN extra=$EXTRA\"\nexit 3\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	h := &HC{Bin: bin}
	err := h.Spawn(context.Background(), testRequest())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("token leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "token=[redacted] extra=1") {
		t.Fatalf("env not passed to process: %v", err)
	}
}

func TestExecRunnerDoesNotLeakServerEnv(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakehc")
	out := filepath.Join(dir, "env.out")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nenv >\"$FAKE_HC_OUT\"\n"), 0o700); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	t.Setenv("FAKE_HC_OUT", out)
	t.Setenv("SUBSPACE_OWNER_TOKEN", "owner-secret-0123456789abcdef")
	t.Setenv("SUBSPACE_OWNER_TOKEN_FILE", "/run/secrets/owner")
	if err := (&HC{Bin: bin}).Spawn(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"SUBSPACE_OWNER_TOKEN", "owner-secret"} {
		if strings.Contains(string(got), leak) {
			t.Errorf("child env contains %q", leak)
		}
	}
	if !strings.Contains(string(got), "SUBSPACE_TOKEN="+testToken) {
		t.Errorf("agent token missing from child env:\n%s", got)
	}
}

func TestExecRunnerRedactsTokenAcrossTruncation(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "fakehc")
	script := "#!/bin/sh\nhead -c 505 /dev/zero | tr '\\000' x\necho \"$SUBSPACE_TOKEN\"\nexit 3\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	err := (&HC{Bin: bin}).Spawn(context.Background(), testRequest())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), testToken[:7]) {
		t.Fatalf("token prefix leaked: %v", err)
	}
}

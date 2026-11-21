package spawner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeHC struct {
	t   *testing.T
	dir string
	bin string
}

func newFakeHC(t *testing.T) *fakeHC {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"hc", "ssh"} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o700); err != nil { //nolint:gosec
			t.Fatal(err)
		}
	}
	state := filepath.Join(dir, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_HC_DIR", state)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &fakeHC{t: t, dir: state, bin: filepath.Join(dir, "hc")}
}

func (f *fakeHC) calls() [][]string {
	f.t.Helper()
	files, err := filepath.Glob(filepath.Join(f.dir, "*.args"))
	if err != nil {
		f.t.Fatal(err)
	}
	var out [][]string
	for _, p := range files {
		data, err := os.ReadFile(p) //nolint:gosec
		if err != nil {
			f.t.Fatal(err)
		}
		out = append(out, strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00"))
	}
	return out
}

func (f *fakeHC) only() []string {
	f.t.Helper()
	c := f.calls()
	if len(c) != 1 {
		f.t.Fatalf("want one hc call, got %q", c)
	}
	return c[0]
}

func (f *fakeHC) hosts() []string {
	data, _ := os.ReadFile(filepath.Join(f.dir, "hosts")) //nolint:gosec
	return strings.Fields(string(data))
}

func (f *fakeHC) maxConcurrent() int {
	data, _ := os.ReadFile(filepath.Join(f.dir, "conc")) //nolint:gosec
	m := 0
	for _, l := range strings.Fields(string(data)) {
		n, _ := strconv.Atoi(l)
		m = max(m, n)
	}
	return m
}

func (f *fakeHC) reset() {
	f.t.Helper()
	files, _ := filepath.Glob(filepath.Join(f.dir, "*"))
	for _, p := range files {
		_ = os.Remove(p)
	}
}

var hostile = []string{
	"it's \"quoted\" $(touch PWNED) `touch PWNED` ; touch PWNED && x | y",
	"--flag=value",
	"line1\nline2\ttab \\ back",
	"-oProxyCommand=touch PWNED",
	"{agent_id} {model} {text}",
}

func TestHCHostileValuesArriveAsSingleArgs(t *testing.T) {
	for _, host := range []string{"", "builder@box"} {
		t.Run("host="+host, func(t *testing.T) {
			f := newFakeHC(t)
			h := &HC{Bin: f.bin, Host: host, CommandTmpl: "--name {agent_id} --model={model} -- {kind} {join_line}"}
			for _, v := range hostile {
				f.reset()
				r := testRequest()
				r.Model, r.JoinLine = v, v
				if err := h.Spawn(context.Background(), r); err != nil {
					t.Fatal(err)
				}
				want := []string{"run", "--bg", "--name", r.AgentID, "--model=" + v, "--", r.Kind, v}
				if got := f.only(); !reflect.DeepEqual(got, want) {
					t.Fatalf("model %q:\ngot  %q\nwant %q", v, got, want)
				}
				if _, err := os.Stat("PWNED"); err == nil {
					t.Fatal("injected command ran")
				}
				if _, err := os.Stat(filepath.Join(f.dir, "PWNED")); err == nil {
					t.Fatal("injected command ran")
				}
			}
		})
	}
}

func TestHCHostileAgentIDsNeverReachTheCLI(t *testing.T) {
	f := newFakeHC(t)
	h := &HC{Bin: f.bin, Host: "builder@box"}
	for _, id := range []string{"x; touch PWNED", "$(id)", "a b", "-rf", "A", "", "a'b", "a\nb", strings.Repeat("a", 64)} {
		r := testRequest()
		r.AgentID = id
		if err := h.Spawn(context.Background(), r); err == nil {
			t.Errorf("spawn accepted %q", id)
		}
		if err := h.Nudge(context.Background(), id, "hi"); err == nil {
			t.Errorf("nudge accepted %q", id)
		}
		if err := h.Stop(context.Background(), id); err == nil {
			t.Errorf("stop accepted %q", id)
		}
		if _, err := h.Status(context.Background(), id); err == nil {
			t.Errorf("status accepted %q", id)
		}
	}
	if c := f.calls(); len(c) != 0 {
		t.Fatalf("hc was invoked: %q", c)
	}
}

func TestHCRejectsHostileHosts(t *testing.T) {
	f := newFakeHC(t)
	for _, host := range []string{"-oProxyCommand=x", "a b", "ok,-bad", "host;rm", ",", "a\nb"} {
		h := &HC{Bin: f.bin, Host: host}
		if err := h.Nudge(context.Background(), "claude-1", "hi"); err == nil {
			t.Errorf("host %q accepted", host)
		}
	}
	if c := f.calls(); len(c) != 0 {
		t.Fatalf("hc was invoked: %q", c)
	}
}

func TestHCNudge(t *testing.T) {
	for _, host := range []string{"", "builder@box"} {
		t.Run("host="+host, func(t *testing.T) {
			f := newFakeHC(t)
			h := &HC{Bin: f.bin, Host: host}
			for _, text := range hostile {
				f.reset()
				if err := h.Nudge(context.Background(), "claude-1", text); err != nil {
					t.Fatal(err)
				}
				if got, want := f.only(), []string{"send", "claude-1", "--", text}; !reflect.DeepEqual(got, want) {
					t.Fatalf("got %q want %q", got, want)
				}
			}
			h.NudgeTmpl = "type {agent_id} --text={text} --enter"
			f.reset()
			if err := h.Nudge(context.Background(), "claude-1", "go on"); err != nil {
				t.Fatal(err)
			}
			if got, want := f.only(), []string{"type", "claude-1", "--text=go on", "--enter"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("got %q want %q", got, want)
			}
		})
	}
}

func TestHCNudgeRejectsUnsafeText(t *testing.T) {
	f := newFakeHC(t)
	h := &HC{Bin: f.bin}
	for _, text := range []string{"", "  \n", "esc\x1b[2J", "nul\x00byte", "bell\a", "cr\rreturn", "bad\xffutf8", "sep\u2028x", strings.Repeat("a", MaxNudgeBytes+1)} {
		if err := h.Nudge(context.Background(), "claude-1", text); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
	if c := f.calls(); len(c) != 0 {
		t.Fatalf("hc was invoked: %q", c)
	}
	if err := h.Nudge(context.Background(), "claude-1", strings.Repeat("a", MaxNudgeBytes)); err != nil {
		t.Fatal(err)
	}
}

func TestHCStatusParsesListings(t *testing.T) {
	cases := []struct {
		name, listing string
		want          Status
		wantErr       bool
	}{
		{"table running", "NAME STATUS\nclaude-1 running 3m\nclaude-2 running 1m\n", StatusRunning, false},
		{"table exited", "NAME STATUS\nclaude-1 exited 0\nclaude-2 running\n", StatusExited, false},
		{"absent", "NAME STATUS\nclaude-2 running\n", StatusExited, false},
		{"prefix id does not match", "claude-10 running\nxclaude-1 running\n", StatusExited, false},
		{"punctuation", "* [claude-1]: dead (exit 1)\n", StatusExited, false},
		{"live word wins", "claude-1 idle done-with-step\n", StatusRunning, false},
		{"bare name", "claude-1\n", StatusRunning, false},
		{"json strings", `["claude-2","claude-1"]`, StatusRunning, false},
		{"json objects", `{"agents":[{"name":"claude-1","state":"exited"},{"name":"claude-2","state":"running"}]}`, StatusExited, false},
		{"json running flag", `[{"id":"claude-1","running":false}]`, StatusExited, false},
		{"json alive", `[{"agent":"claude-1","status":"Running"}]`, StatusRunning, false},
		{"json empty", `[]`, StatusExited, false},
		{"json absent", `[{"name":"claude-2"}]`, StatusExited, false},
		{"empty output", "", StatusUnknown, true},
		{"blank output", " \n\n", StatusUnknown, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeHC(t)
			ls := filepath.Join(t.TempDir(), "ls")
			if err := os.WriteFile(ls, []byte(c.listing), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("FAKE_HC_LS", ls)
			got, err := (&HC{Bin: f.bin}).Status(context.Background(), "claude-1")
			if (err != nil) != c.wantErr || got != c.want {
				t.Fatalf("got %s, %v; want %s, err=%v", got, err, c.want, c.wantErr)
			}
			if args := f.only(); !reflect.DeepEqual(args, []string{"ls"}) {
				t.Fatalf("args %q", args)
			}
		})
	}
}

func TestHCStatusNeverGuessesExitFromABrokenListing(t *testing.T) {
	f := newFakeHC(t)
	t.Setenv("FAKE_HC_MODE", "huge")
	h := &HC{Bin: f.bin, MaxOutput: 1024}
	st, err := h.Status(context.Background(), "claude-1")
	if err == nil || st != StatusUnknown {
		t.Fatalf("got %s, %v", st, err)
	}

	t.Setenv("FAKE_HC_MODE", "fail")
	st, err = h.Status(context.Background(), "claude-1")
	if err == nil || st != StatusUnknown {
		t.Fatalf("got %s, %v", st, err)
	}

	t.Setenv("FAKE_HC_MODE", "")
	ls := filepath.Join(t.TempDir(), "ls")
	if err := os.WriteFile(ls, []byte(strings.Repeat("claude-2 running\n", 200)+"claude-1 running\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_HC_LS", ls)
	st, err = (&HC{Bin: f.bin, MaxOutput: 512}).Status(context.Background(), "claude-1")
	if err == nil || st != StatusUnknown {
		t.Fatalf("truncated listing without the agent: %s, %v", st, err)
	}
	st, err = (&HC{Bin: f.bin, MaxOutput: 512}).Status(context.Background(), "claude-2")
	if err != nil || st != StatusRunning {
		t.Fatalf("truncated listing with the agent: %s, %v", st, err)
	}
}

func TestHCStop(t *testing.T) {
	f := newFakeHC(t)
	h := &HC{Bin: f.bin, StopTmpl: "kill --force {agent_id}"}
	if err := h.Stop(context.Background(), "claude-1"); err != nil {
		t.Fatal(err)
	}
	if got, want := f.only(), []string{"kill", "--force", "claude-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}

	t.Setenv("FAKE_HC_MODE", "gone")
	if err := h.Stop(context.Background(), "claude-1"); err != nil {
		t.Fatalf("an agent that is already gone must stop cleanly: %v", err)
	}
	t.Setenv("FAKE_HC_MODE", "fail")
	if err := h.Stop(context.Background(), "claude-1"); err == nil {
		t.Fatal("stop failure swallowed")
	}
}

func TestHCTimeoutKillsTheProcess(t *testing.T) {
	f := newFakeHC(t)
	t.Setenv("FAKE_HC_MODE", "sleep")
	h := &HC{Bin: f.bin, Timeout: 200 * time.Millisecond}
	start := time.Now()
	err := h.Nudge(context.Background(), "claude-1", "hi")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("took %s", d)
	}
	if _, err := h.Status(context.Background(), "claude-1"); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("status err %v", err)
	}
}

func TestHCHugeOutputIsCapped(t *testing.T) {
	f := newFakeHC(t)
	t.Setenv("FAKE_HC_MODE", "huge")
	h := &HC{Bin: f.bin, MaxOutput: 4096}
	if err := h.Spawn(context.Background(), testRequest()); err != nil {
		t.Fatalf("a chatty successful run must still succeed: %v", err)
	}

	t.Setenv("FAKE_HC_MODE", "fail")
	err := h.Spawn(context.Background(), testRequest())
	if err == nil || strings.Contains(err.Error(), testToken) || !strings.Contains(err.Error(), "boom: [redacted]") {
		t.Fatalf("err %v", err)
	}
	if len(err.Error()) > 700 {
		t.Fatalf("error not capped: %d bytes", len(err.Error()))
	}
}

func TestHCPerHostConcurrencyLimit(t *testing.T) {
	f := newFakeHC(t)
	t.Setenv("FAKE_HC_MODE", "slow")
	h := &HC{Bin: f.bin, MaxPerHost: 2}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- h.Nudge(context.Background(), fmt.Sprintf("claude-%d", i), "hi")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := f.maxConcurrent(); got != 2 {
		t.Fatalf("max concurrent hc processes %d, want 2", got)
	}
	if n := len(f.calls()); n != 8 {
		t.Fatalf("%d calls", n)
	}
}

func TestHCQueueWaitHonoursContext(t *testing.T) {
	f := newFakeHC(t)
	t.Setenv("FAKE_HC_MODE", "sleep")
	h := &HC{Bin: f.bin, MaxPerHost: 1, Timeout: 5 * time.Second}
	go func() { _ = h.Nudge(context.Background(), "claude-1", "hold") }()
	deadline := time.Now().Add(5 * time.Second)
	for len(f.calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := h.Nudge(ctx, "claude-2", "queued")
	if err == nil || !strings.Contains(err.Error(), "waiting for a slot") {
		t.Fatalf("err %v", err)
	}
	if n := len(f.calls()); n != 1 {
		t.Fatalf("queued call ran: %d", n)
	}
}

func TestHCHostSelectionIsStablePerAgent(t *testing.T) {
	f := newFakeHC(t)
	h := &HC{Bin: f.bin, Host: "a@one, b@two ,c@three"}
	ls := filepath.Join(t.TempDir(), "ls")
	if err := os.WriteFile(ls, []byte("other running\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_HC_LS", ls)
	ids := make([]string, 30)
	for i := range ids {
		ids[i] = fmt.Sprintf("claude-%02d", i)
	}
	hostOf := func(do func(id string) error, id string) string {
		t.Helper()
		f.reset()
		if err := do(id); err != nil {
			t.Fatal(err)
		}
		hs := f.hosts()
		if len(hs) != 1 {
			t.Fatalf("hosts %q", hs)
		}
		return hs[0]
	}
	seen := map[string]bool{}
	for _, id := range ids {
		first := hostOf(func(id string) error { return h.Nudge(context.Background(), id, "hi") }, id)
		seen[first] = true
		for name, do := range map[string]func(string) error{
			"stop":   func(id string) error { return h.Stop(context.Background(), id) },
			"status": func(id string) error { _, err := h.Status(context.Background(), id); return err },
			"spawn": func(id string) error {
				r := testRequest()
				r.AgentID = id
				return h.Spawn(context.Background(), r)
			},
		} {
			if got := hostOf(do, id); got != first {
				t.Fatalf("%s %s went to %s, nudge went to %s", name, id, got, first)
			}
		}
	}
	want := []string{"a@one", "b@two", "c@three"}
	for _, w := range want {
		if !seen[w] {
			t.Errorf("host %s never selected: %v", w, seen)
		}
	}
	for s := range seen {
		if !slices.Contains(want, s) {
			t.Errorf("unexpected host %q", s)
		}
	}
}

func TestNewHCSettings(t *testing.T) {
	env := map[string]string{
		"SUBSPACE_HC_NUDGE_COMMAND": "type {agent_id} {text}", "SUBSPACE_HC_STATUS_COMMAND": "list",
		"SUBSPACE_HC_STOP_COMMAND": "rm {agent_id}", "SUBSPACE_HC_TIMEOUT": "5s",
		"SUBSPACE_HC_MAX_OUTPUT": "2048", "SUBSPACE_HC_MAX_PER_HOST": "3",
	}
	s, err := New("hc", func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	h := s.(*HC)
	if h.NudgeTmpl != "type {agent_id} {text}" || h.StatusTmpl != "list" || h.StopTmpl != "rm {agent_id}" ||
		h.Timeout != 5*time.Second || h.MaxOutput != 2048 || h.MaxPerHost != 3 {
		t.Fatalf("%#v", h)
	}
	if _, ok := s.(Nudger); !ok {
		t.Fatal("hc spawner must be a Nudger")
	}
	for _, bad := range []map[string]string{
		{"SUBSPACE_HC_TIMEOUT": "soon"}, {"SUBSPACE_HC_TIMEOUT": "-1s"},
		{"SUBSPACE_HC_MAX_OUTPUT": "big"}, {"SUBSPACE_HC_MAX_PER_HOST": "-2"},
	} {
		if _, err := New("hc", func(k string) string { return bad[k] }); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

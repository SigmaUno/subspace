package spawner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	DefaultHCCommand    = "--name {agent_id} -- {kind} {join_line}"
	DefaultHCNudge      = "send {agent_id} -- {text}"
	DefaultHCStatus     = "ls"
	DefaultHCStop       = "kill {agent_id}"
	DefaultHCTimeout    = 30 * time.Second
	DefaultHCMaxOutput  = 1 << 20
	DefaultHCMaxPerHost = 4

	MaxNudgeBytes = 4096
)

var (
	agentIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	envKeyRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	hostRe    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@:%\[\]+-]{0,254}$`)
)

type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

type envCtxKey struct{}

func EnvFromContext(ctx context.Context) []string {
	e, _ := ctx.Value(envCtxKey{}).([]string)
	return e
}

type HC struct {
	Bin         string
	Host        string
	CommandTmpl string
	NudgeTmpl   string
	StatusTmpl  string
	StopTmpl    string
	Timeout     time.Duration
	MaxOutput   int
	MaxPerHost  int
	Runner      Runner

	mu    sync.Mutex
	slots map[string]chan struct{}
}

func (*HC) Name() string { return "hc" }

func (h *HC) Spawn(ctx context.Context, r Request) error {
	if !agentIDRe.MatchString(r.AgentID) {
		return fmt.Errorf("spawner hc: invalid agent id %q", r.AgentID)
	}
	host, err := h.hostFor(r.AgentID)
	if err != nil {
		return err
	}
	env, err := hcEnv(r)
	if err != nil {
		return err
	}
	rep := strings.NewReplacer("{agent_id}", r.AgentID, "{kind}", r.Kind, "{model}", r.Model, "{join_line}", r.JoinLine)
	args := append([]string{"run", "--bg"}, expand(h.CommandTmpl, DefaultHCCommand, rep)...)
	if _, err := h.do(ctx, host, args, env, r.Token); err != nil {
		return errors.New("spawner hc: " + err.Error())
	}
	return nil
}

func (h *HC) Nudge(ctx context.Context, agentID, text string) error {
	if !agentIDRe.MatchString(agentID) {
		return fmt.Errorf("spawner hc: invalid agent id %q", agentID)
	}
	if err := validNudge(text); err != nil {
		return err
	}
	host, err := h.hostFor(agentID)
	if err != nil {
		return err
	}
	rep := strings.NewReplacer("{agent_id}", agentID, "{text}", text)
	if _, err := h.do(ctx, host, expand(h.NudgeTmpl, DefaultHCNudge, rep), nil, ""); err != nil {
		return errors.New("spawner hc: nudge: " + err.Error())
	}
	return nil
}

func (h *HC) Stop(ctx context.Context, agentID string) error {
	if !agentIDRe.MatchString(agentID) {
		return fmt.Errorf("spawner hc: invalid agent id %q", agentID)
	}
	host, err := h.hostFor(agentID)
	if err != nil {
		return err
	}
	rep := strings.NewReplacer("{agent_id}", agentID)
	_, err = h.do(ctx, host, expand(h.StopTmpl, DefaultHCStop, rep), nil, "")
	if err != nil && !goneRe.MatchString(err.Error()) {
		return errors.New("spawner hc: stop: " + err.Error())
	}
	return nil
}

func (h *HC) Status(ctx context.Context, agentID string) (Status, error) {
	if !agentIDRe.MatchString(agentID) {
		return StatusUnknown, fmt.Errorf("spawner hc: invalid agent id %q", agentID)
	}
	host, err := h.hostFor(agentID)
	if err != nil {
		return StatusUnknown, err
	}
	rep := strings.NewReplacer("{agent_id}", agentID)
	out, err := h.do(ctx, host, expand(h.StatusTmpl, DefaultHCStatus, rep), nil, "")
	if err != nil {
		return StatusUnknown, errors.New("spawner hc: status: " + err.Error())
	}
	st, err := parseListing(out, agentID, len(out) >= h.maxOutput())
	if err != nil {
		return StatusUnknown, errors.New("spawner hc: status: " + err.Error())
	}
	return st, nil
}

var goneRe = regexp.MustCompile(`(?i)no such|not found|unknown agent|does not exist`)

func validNudge(text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("spawner hc: empty nudge text")
	}
	if len(text) > MaxNudgeBytes {
		return fmt.Errorf("spawner hc: nudge text over %d bytes", MaxNudgeBytes)
	}
	if !utf8.ValidString(text) {
		return errors.New("spawner hc: nudge text is not valid UTF-8")
	}
	for _, r := range text {
		if r != '\n' && r != '\t' && (unicode.IsControl(r) || r == '\u2028' || r == '\u2029') {
			return errors.New("spawner hc: nudge text contains control characters")
		}
	}
	return nil
}

func (h *HC) hostFor(agentID string) (string, error) {
	var hosts []string
	for _, p := range strings.Split(h.Host, ",") {
		if p = strings.TrimSpace(p); p != "" {
			if !hostRe.MatchString(p) {
				return "", errors.New("spawner hc: invalid host")
			}
			hosts = append(hosts, p)
		}
	}
	switch len(hosts) {
	case 0:
		if strings.TrimSpace(h.Host) != "" {
			return "", errors.New("spawner hc: invalid host")
		}
		return "", nil
	case 1:
		return hosts[0], nil
	}
	idx := 0
	for _, b := range []byte(agentID) {
		idx = (idx*31 + int(b)) % len(hosts)
	}
	return hosts[idx], nil
}

func (h *HC) acquire(ctx context.Context, host string) (func(), error) {
	h.mu.Lock()
	if h.slots == nil {
		h.slots = map[string]chan struct{}{}
	}
	ch, ok := h.slots[host]
	if !ok {
		n := h.MaxPerHost
		if n <= 0 {
			n = DefaultHCMaxPerHost
		}
		ch = make(chan struct{}, n)
		h.slots[host] = ch
	}
	h.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for a slot on %s: %w", hostLabel(host), ctx.Err())
	}
}

func hostLabel(host string) string {
	if host == "" {
		return "local host"
	}
	return host
}

func (h *HC) timeout() time.Duration {
	if h.Timeout > 0 {
		return h.Timeout
	}
	return DefaultHCTimeout
}

func (h *HC) maxOutput() int {
	if h.MaxOutput > 0 {
		return h.MaxOutput
	}
	return DefaultHCMaxOutput
}

func (h *HC) do(ctx context.Context, host string, hcArgs, env []string, token string) ([]byte, error) {
	release, err := h.acquire(ctx, host)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, h.timeout())
	defer cancel()
	name, args := h.wrap(host, hcArgs, env)
	run := h.Runner
	if run == nil {
		run = execRunner(token, h.maxOutput())
	}
	out, err := run(context.WithValue(ctx, envCtxKey{}, env), name, args...)
	if err == nil {
		return out, nil
	}
	msg := err.Error()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		msg = fmt.Sprintf("timed out after %s", h.timeout())
	}
	if token != "" {
		msg = strings.ReplaceAll(msg, token, "[redacted]")
	}
	return nil, errors.New(msg)
}

func expand(tmpl, def string, rep *strings.Replacer) []string {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = def
	}
	fields := strings.Fields(tmpl)
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = rep.Replace(f)
	}
	return out
}

func hcEnv(r Request) ([]string, error) {
	m := map[string]string{}
	for k, v := range r.Env {
		if !envKeyRe.MatchString(k) {
			return nil, fmt.Errorf("spawner hc: invalid env key %q", k)
		}
		m[k] = v
	}
	m["SUBSPACE_TOKEN"] = r.Token
	m["SUBSPACE_URL"] = r.MCPURL
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, k+"="+m[k])
	}
	return env, nil
}

func (h *HC) wrap(host string, hcArgs, env []string) (string, []string) {
	bin := h.Bin
	if bin == "" {
		bin = "hc"
	}
	if host == "" {
		return bin, hcArgs
	}
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10"}
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		args = append(args, "-o", "SendEnv="+k)
	}
	quoted := make([]string, 0, len(hcArgs)+1)
	for _, a := range append([]string{bin}, hcArgs...) {
		quoted = append(quoted, shellQuote(a))
	}
	args = append(args, "--", host, strings.Join(quoted, " "))
	return "ssh", args
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

var (
	liveWords = map[string]bool{"running": true, "alive": true, "active": true, "busy": true, "idle": true, "working": true, "up": true}
	deadWords = map[string]bool{"exited": true, "dead": true, "stopped": true, "finished": true, "terminated": true, "failed": true, "killed": true, "crashed": true, "gone": true, "done": true}
	nameKeys  = []string{"name", "id", "agent", "agent_id", "title", "label"}
	stateKeys = []string{"status", "state"}
)

func parseListing(out []byte, id string, truncated bool) (Status, error) {
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return StatusUnknown, errors.New("empty listing")
	}
	if trimmed[0] == '[' || trimmed[0] == '{' {
		var v any
		if err := json.Unmarshal(trimmed, &v); err == nil {
			if st, found := walkListing(v, id, 0); found {
				return st, nil
			}
			if truncated {
				return StatusUnknown, errors.New("listing truncated")
			}
			return StatusExited, nil
		}
	}
	for _, line := range strings.Split(string(out), "\n") {
		words := strings.FieldsFunc(line, notWordRune)
		i := slices.Index(words, id)
		if i < 0 {
			continue
		}
		live, dead := false, false
		for j, w := range words {
			if j == i {
				continue
			}
			w = strings.ToLower(w)
			live = live || liveWords[w]
			dead = dead || deadWords[w]
		}
		if dead && !live {
			return StatusExited, nil
		}
		return StatusRunning, nil
	}
	if truncated {
		return StatusUnknown, errors.New("listing truncated")
	}
	return StatusExited, nil
}

func notWordRune(r rune) bool {
	return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' && r != '.'
}

func walkListing(v any, id string, depth int) (Status, bool) {
	if depth > 8 {
		return StatusUnknown, false
	}
	switch x := v.(type) {
	case string:
		if x == id {
			return StatusRunning, true
		}
	case []any:
		for _, e := range x {
			if st, ok := walkListing(e, id, depth+1); ok {
				return st, true
			}
		}
	case map[string]any:
		for _, k := range nameKeys {
			if s, ok := x[k].(string); ok && s == id {
				return entryStatus(x), true
			}
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if st, ok := walkListing(x[k], id, depth+1); ok {
				return st, true
			}
		}
	}
	return StatusUnknown, false
}

func entryStatus(m map[string]any) Status {
	for _, k := range stateKeys {
		if s, ok := m[k].(string); ok {
			w := strings.ToLower(s)
			if deadWords[w] && !liveWords[w] {
				return StatusExited
			}
			return StatusRunning
		}
	}
	if b, ok := m["running"].(bool); ok && !b {
		return StatusExited
	}
	if b, ok := m["alive"].(bool); ok && !b {
		return StatusExited
	}
	return StatusRunning
}

func execRunner(token string, limit int) Runner {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // operator-configured binary
		cmd.Env = append(childEnv(), EnvFromContext(ctx)...)
		return runCapture(cmd, token, limit)
	}
}

const maxCapture = 64 << 10

func childEnv() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "SUBSPACE_") })
}

type headBuffer struct {
	buf   []byte
	limit int
}

func (h *headBuffer) Write(p []byte) (int, error) {
	if n := h.limit - len(h.buf); n > 0 {
		h.buf = append(h.buf, p[:min(n, len(p))]...)
	}
	return len(p), nil
}

func runCmd(cmd *exec.Cmd, token string) error {
	_, err := runCapture(cmd, token, maxCapture)
	return err
}

func runCapture(cmd *exec.Cmd, token string, limit int) ([]byte, error) {
	out := &headBuffer{limit: limit}
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if err == nil {
		return out.buf, nil
	}
	raw := out.buf
	if len(raw) == limit {
		raw = raw[:max(len(raw)-len(token), 0)]
	}
	msg := strings.TrimSpace(redact(string(raw), token))
	if len(msg) > 512 {
		msg = msg[:512]
	}
	if msg != "" {
		return nil, fmt.Errorf("%s: %w: %s", cmd.Args[0], err, msg)
	}
	return nil, fmt.Errorf("%s: %w", cmd.Args[0], err)
}

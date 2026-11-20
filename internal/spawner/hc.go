package spawner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

const DefaultHCCommand = "--name {agent_id} -- {kind} {join_line}"

var (
	agentIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	envKeyRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type Runner func(ctx context.Context, name string, args ...string) error

type envCtxKey struct{}

func EnvFromContext(ctx context.Context) []string {
	e, _ := ctx.Value(envCtxKey{}).([]string)
	return e
}

type HC struct {
	Bin         string
	Host        string
	CommandTmpl string
	Runner      Runner
}

func (*HC) Name() string { return "hc" }

func (h *HC) Spawn(ctx context.Context, r Request) error {
	if !agentIDRe.MatchString(r.AgentID) {
		return fmt.Errorf("spawner hc: invalid agent id %q", r.AgentID)
	}
	if strings.HasPrefix(h.Host, "-") {
		return errors.New("spawner hc: invalid host")
	}
	env, err := hcEnv(r)
	if err != nil {
		return err
	}
	name, args := h.command(r, env)
	run := h.Runner
	if run == nil {
		run = execRunner(r.Token)
	}
	ctx = context.WithValue(ctx, envCtxKey{}, env)
	if err := run(ctx, name, args...); err != nil {
		msg := err.Error()
		if r.Token != "" {
			msg = strings.ReplaceAll(msg, r.Token, "[redacted]")
		}
		return errors.New("spawner hc: " + msg)
	}
	return nil
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

func (h *HC) command(r Request, env []string) (string, []string) {
	bin := h.Bin
	if bin == "" {
		bin = "hc"
	}
	tmpl := h.CommandTmpl
	if strings.TrimSpace(tmpl) == "" {
		tmpl = DefaultHCCommand
	}
	hcArgs := []string{"run", "--bg"}
	rep := strings.NewReplacer("{agent_id}", r.AgentID, "{kind}", r.Kind, "{model}", r.Model, "{join_line}", r.JoinLine)
	for _, f := range strings.Fields(tmpl) {
		hcArgs = append(hcArgs, rep.Replace(f))
	}
	if h.Host == "" {
		return bin, hcArgs
	}

	args := []string{"-o", "BatchMode=yes"}
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		args = append(args, "-o", "SendEnv="+k)
	}
	quoted := make([]string, 0, len(hcArgs)+1)
	for _, a := range append([]string{bin}, hcArgs...) {
		quoted = append(quoted, shellQuote(a))
	}
	args = append(args, "--", h.Host, strings.Join(quoted, " "))
	return "ssh", args
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func execRunner(token string) Runner {
	return func(ctx context.Context, name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // operator-configured binary
		cmd.Env = append(os.Environ(), EnvFromContext(ctx)...)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(out.String())
			if len(msg) > 512 {
				msg = msg[:512]
			}
			if token != "" {
				msg = strings.ReplaceAll(msg, token, "[redacted]")
			}
			if msg != "" {
				return fmt.Errorf("%s: %w: %s", name, err, msg)
			}
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	}
}

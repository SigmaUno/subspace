package spawner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
)

const (
	DefaultOpenCodeSession = "swarm"
	DefaultOpenCodeBin     = "opencode"
)

var (
	sessionRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	repoRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)*$`)
	modelRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]*$`)
	absPathRe = regexp.MustCompile(`^/[A-Za-z0-9._/+-]*$`)
)

var openCodeProviders = map[string]string{
	"opencode-anthropic": "anthropic",
	"opencode-deepseek":  "deepseek",
}

const openCodeScript = `set -eu
umask 077
base=$1 wt=$2 session=$3 window=$4 bin=$5 model=$6 prompt=$7
envf=$(mktemp)
started=0
trap '[ "$started" = 1 ] || rm -f -- "$envf"' EXIT
cat >"$envf"
if [ ! -d "$wt" ]; then
	mkdir -p -- "$(dirname -- "$wt")"
	if [ -n "$base" ]; then
		git -C "$base" worktree add --detach -- "$wt"
	else
		mkdir -- "$wt"
	fi
fi
launch='set -a; . "$1"; rm -f -- "$1"; set +a; shift; exec "$@"'
if tmux has-session -t "=$session" 2>/dev/null; then
	tmux new-window -d -t "$session:" -n "$window" -c "$wt" -- sh -c "$launch" sh "$envf" "$bin" run --auto -m "$model" "$prompt"
else
	tmux new-session -d -s "$session" -n "$window" -c "$wt" -- sh -c "$launch" sh "$envf" "$bin" run --auto -m "$model" "$prompt"
fi
started=1
`

type Exec func(ctx context.Context, stdin []byte, name string, args ...string) error

type OpenCode struct {
	Host     string
	Root     string
	ReposDir string
	Session  string
	Bin      string
	Exec     Exec
}

func (*OpenCode) Name() string { return "opencode" }

func (o *OpenCode) Spawn(ctx context.Context, r Request) error {
	script, args, stdin, err := o.plan(r)
	if err != nil {
		return err
	}
	run := o.Exec
	if run == nil {
		run = execStdin(r.Token)
	}
	name, argv := "sh", append([]string{"-c", script, "sh"}, args...)
	if o.Host != "" {
		quoted := make([]string, 0, len(argv)+1)
		for _, a := range append([]string{"sh"}, argv...) {
			quoted = append(quoted, shellQuote(a))
		}
		name, argv = "ssh", []string{"-o", "BatchMode=yes", "--", o.Host, strings.Join(quoted, " ")}
	}
	if err := run(ctx, stdin, name, argv...); err != nil {
		msg := err.Error()
		if r.Token != "" {
			msg = strings.ReplaceAll(msg, r.Token, "[redacted]")
		}
		return errors.New("spawner opencode: " + msg)
	}
	return nil
}

func (o *OpenCode) plan(r Request) (string, []string, []byte, error) {
	if !agentIDRe.MatchString(r.AgentID) {
		return "", nil, nil, fmt.Errorf("spawner opencode: invalid agent id %q", r.AgentID)
	}
	provider, ok := openCodeProviders[r.Kind]
	if !ok {
		return "", nil, nil, fmt.Errorf("spawner opencode: unsupported kind %q", r.Kind)
	}
	if r.Provider != "" && r.Provider != provider {
		return "", nil, nil, fmt.Errorf("spawner opencode: kind %q requires provider %q, got %q", r.Kind, provider, r.Provider)
	}
	if strings.HasPrefix(o.Host, "-") {
		return "", nil, nil, errors.New("spawner opencode: invalid host")
	}
	if !absPathRe.MatchString(o.Root) || strings.Contains(o.Root, "..") {
		return "", nil, nil, fmt.Errorf("spawner opencode: root must be an absolute path, got %q", o.Root)
	}
	session := o.Session
	if session == "" {
		session = DefaultOpenCodeSession
	}
	if !sessionRe.MatchString(session) {
		return "", nil, nil, fmt.Errorf("spawner opencode: invalid tmux session %q", session)
	}
	bin := o.Bin
	if bin == "" {
		bin = DefaultOpenCodeBin
	}
	mdl := r.Model
	if !modelRe.MatchString(mdl) {
		return "", nil, nil, fmt.Errorf("spawner opencode: invalid model %q", r.Model)
	}
	if !strings.Contains(mdl, "/") {
		mdl = provider + "/" + mdl
	}
	if strings.TrimSpace(r.JoinLine) == "" {
		return "", nil, nil, errors.New("spawner opencode: empty join line")
	}
	base := ""
	if r.Repo != "" {
		if !repoRe.MatchString(r.Repo) || strings.Contains(r.Repo, "..") {
			return "", nil, nil, fmt.Errorf("spawner opencode: invalid repo %q", r.Repo)
		}
		repos := o.ReposDir
		if repos == "" {
			repos = path.Join(o.Root, ".repos")
		}
		if !absPathRe.MatchString(repos) || strings.Contains(repos, "..") {
			return "", nil, nil, fmt.Errorf("spawner opencode: repos dir must be an absolute path, got %q", repos)
		}
		base = path.Join(repos, r.Repo)
	}
	env, err := openCodeEnv(r)
	if err != nil {
		return "", nil, nil, err
	}
	args := []string{base, path.Join(o.Root, r.AgentID), session, r.AgentID, bin, mdl, r.JoinLine}
	return openCodeScript, args, env, nil
}

func openCodeEnv(r Request) ([]byte, error) {
	m := map[string]string{}
	for k, v := range r.Env {
		if !envKeyRe.MatchString(k) {
			return nil, fmt.Errorf("spawner opencode: invalid env key %q", k)
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
	var b bytes.Buffer
	for _, k := range keys {
		b.WriteString(k + "=" + shellQuote(m[k]) + "\n")
	}
	return b.Bytes(), nil
}

func execStdin(token string) Exec {
	return func(ctx context.Context, stdin []byte, name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // operator-configured host and fixed script
		cmd.Stdin = bytes.NewReader(stdin)
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

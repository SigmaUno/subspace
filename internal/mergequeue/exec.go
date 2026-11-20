package mergequeue

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
)

type ExecRunner struct {
	Env []string
}

func (r ExecRunner) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204
	cmd.Dir = dir
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "SUBSPACE_") })
	cmd.Env = append(append(env, "GIT_TERMINAL_PROMPT=0"), r.Env...)
	out := tailBuffer{max: maxLog}
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return string(out.buf), err
}

type tailBuffer struct {
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	if len(p) >= t.max {
		t.buf = append(t.buf[:0], p[len(p)-t.max:]...)
		return len(p), nil
	}
	if over := len(t.buf) + len(p) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	t.buf = append(t.buf, p...)
	return len(p), nil
}

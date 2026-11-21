package spawner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

type Noop struct {
	Log *slog.Logger
}

func (Noop) Name() string { return "noop" }

func (n Noop) Spawn(ctx context.Context, r Request) error {
	l := n.Log
	if l == nil {
		l = slog.Default()
	}
	l.InfoContext(ctx, "spawn requested (noop)",
		"agent_id", r.AgentID, "kind", r.Kind, "model", r.Model, "provider", r.Provider,
		"repo", r.Repo, "mcp_url", r.MCPURL, "token", redacted(r.Token))
	return nil
}

func (Noop) Stop(context.Context, string) error { return ErrNotImplemented }

func (Noop) Status(context.Context, string) (Status, error) { return StatusUnknown, nil }

func redacted(token string) string {
	if token == "" {
		return ""
	}
	return "[redacted]"
}

func New(name string, env func(string) string) (Spawner, error) {
	if env == nil {
		env = func(string) string { return "" }
	}
	switch name {
	case "", "noop", "log":
		return Noop{}, nil
	case "hc":
		bin := env("SUBSPACE_HC_BIN")
		if bin == "" {
			bin = "hc"
		}
		return &HC{Bin: bin, Host: env("SUBSPACE_HC_HOST"), CommandTmpl: env("SUBSPACE_HC_COMMAND")}, nil
	case "opencode":
		root := env("SUBSPACE_OPENCODE_ROOT")
		if root == "" {
			return nil, errors.New("spawner: SUBSPACE_OPENCODE_ROOT is required for the opencode spawner")
		}
		return &OpenCode{
			Host:     env("SUBSPACE_OPENCODE_HOST"),
			Root:     root,
			ReposDir: env("SUBSPACE_OPENCODE_REPOS"),
			Session:  env("SUBSPACE_OPENCODE_SESSION"),
			Bin:      env("SUBSPACE_OPENCODE_BIN"),
		}, nil
	case "docker":
		return Docker{}, nil
	default:
		return nil, fmt.Errorf("spawner: unknown spawner %q (want noop, hc, opencode or docker)", name)
	}
}

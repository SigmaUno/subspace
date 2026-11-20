package spawner

import (
	"context"
	"fmt"
	"log/slog"
)

// Noop logs a redacted line and does nothing else; the agent is started by hand
// or by an external tool.
type Noop struct {
	Log *slog.Logger
}

// Name implements Spawner.
func (Noop) Name() string { return "noop" }

// Spawn implements Spawner. Token and Env values are never logged.
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

func redacted(token string) string {
	if token == "" {
		return ""
	}
	return "[redacted]"
}

// New builds the spawner selected by name ("", "noop", "log", "hc", "docker").
// env looks up configuration variables (os.Getenv in production).
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
	case "docker":
		return Docker{}, nil
	default:
		return nil, fmt.Errorf("spawner: unknown spawner %q (want noop, hc or docker)", name)
	}
}

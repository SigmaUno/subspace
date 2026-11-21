package reload

import (
	"fmt"
	"log/slog"
	"strings"
)

const EnvLogLevel = "SUBSPACE_LOG_LEVEL"

func ParseLevel(v string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("%s: unknown level %q", EnvLogLevel, v)
}

type LogLevel struct{ Var *slog.LevelVar }

func (l LogLevel) PrepareReload(getenv func(string) string) (Plan, error) {
	lv, err := ParseLevel(getenv(EnvLogLevel))
	if err != nil {
		return Plan{}, err
	}
	return Plan{Commit: func() { l.Var.Set(lv) }}, nil
}

package reload

import (
	"github.com/SigmaUno/subspace/internal/logpipe"
)

const EnvLogLevel = "SUBSPACE_LOG_LEVEL"

type LogLevel struct {
	Pipe    *logpipe.Pipeline
	Applied string
}

func (l *LogLevel) PrepareReload(getenv func(string) string) (Plan, error) {
	v := getenv(EnvLogLevel)
	lv, err := logpipe.ParseLevel(v)
	if err != nil {
		return Plan{}, err
	}
	return Plan{Commit: func() {
		if v == l.Applied {
			return
		}
		l.Applied = v
		l.Pipe.SetLevel(lv)
	}}, nil
}

package audit

import "time"

type LoopGuard interface {
	Hit(agentID, tool, argsHash string, now time.Time) (looping bool)
}

const (
	DefaultMaxRepeats = 5
	DefaultWindow     = 10 * time.Minute
)

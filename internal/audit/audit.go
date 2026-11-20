// Package audit holds loop protection for tool calls.
package audit

import "time"

// LoopGuard detects repeated identical tool calls.
type LoopGuard interface {
	// Hit records a call and reports whether the agent exceeded the limit.
	Hit(agentID, tool, argsHash string, now time.Time) (looping bool)
}

// DefaultMaxRepeats and DefaultWindow: same tool+args >5 times in 10 min.
const (
	DefaultMaxRepeats = 5
	DefaultWindow     = 10 * time.Minute
)

// TODO(split B5): NewLoopGuard(max int, window time.Duration) LoopGuard in loop.go.

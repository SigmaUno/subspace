// Package threads parses and scopes thread references.
package threads

import (
	"strings"

	"github.com/SigmaUno/subspace/internal/model"
)

// Ref identifies a thread.
type Ref struct{ Kind, ID string }

// Parse parses "task:T-1", "agent:me" (resolved with self), "brief:B-1".
func Parse(s, self string) (Ref, error) {
	k, id, ok := strings.Cut(s, ":")
	if !ok || id == "" {
		return Ref{}, model.BadRequest("thread must look like kind:id")
	}
	switch k {
	case model.ThreadAgent:
		if id == "me" {
			id = self
		}
	case model.ThreadTask, model.ThreadBrief:
	default:
		return Ref{}, model.BadRequest("unknown thread kind %q", k)
	}
	return Ref{Kind: k, ID: id}, nil
}

// Topic is the SSE topic of a thread.
func (r Ref) Topic() string { return "thread:" + r.Kind + ":" + r.ID }

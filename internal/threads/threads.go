package threads

import (
	"strings"

	"github.com/SigmaUno/subspace/internal/model"
)

type Ref struct{ Kind, ID string }

func Parse(s, self string) (Ref, error) {
	k, id, ok := strings.Cut(s, ":")
	if !ok || id == "" {
		return Ref{}, model.BadRequest("thread must look like kind:id")
	}
	switch k {
	case model.ThreadAgent:
		if id == "me" && self != "" {
			id = self
		}
	case model.ThreadTask, model.ThreadBrief:
	default:
		return Ref{}, model.BadRequest("unknown thread kind %q", k)
	}
	return Ref{Kind: k, ID: id}, nil
}

func (r Ref) Topic() string { return "thread:" + r.Kind + ":" + r.ID }

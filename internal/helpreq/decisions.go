package helpreq

import (
	"context"
	"fmt"

	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/model"
)

const KindDecision = "help"

func (s *Service) Decisions(ctx context.Context) ([]*decisions.Decision, error) {
	rs, err := s.List(ctx, Filter{Status: StatusOpen})
	if err != nil {
		return nil, err
	}
	var out []*decisions.Decision
	for _, r := range rs {
		if r.Type != TypeHelp {
			continue
		}
		out = append(out, &decisions.Decision{
			ID:        "help:" + r.ID,
			Kind:      KindDecision,
			Title:     fmt.Sprintf("%s needs help on %s: %s", r.AgentID, r.TaskID, r.Kind),
			Summary:   clip(r.Details, maxSummary),
			BriefID:   r.BriefID,
			TaskID:    r.TaskID,
			AgentID:   r.AgentID,
			Links:     decisions.Links{Thread: model.ThreadTask + ":" + r.TaskID},
			CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}

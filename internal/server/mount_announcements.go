package server

import (
	"context"

	"github.com/SigmaUno/subspace/internal/announce"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
)

func init() { registerMount(mountAnnouncements) }

func mountAnnouncements(d Deps) {
	svc := announce.New(announce.Config{Store: d.Store, Audience: announceAudience{d.Board}, Poster: d.Board, Now: d.Cfg.Now})
	mcp.AppendToInstructions(d.Registry, svc.Instructions)
	h := announce.Handler(svc)
	d.API.Mount(d.Mux, "POST /api/v1/announcements", h)
	d.API.Mount(d.Mux, "GET /api/v1/announcements", h)
	d.API.Mount(d.Mux, "DELETE /api/v1/announcements/{id}", h)
}

type announceAudience struct{ b *board.Board }

func (a announceAudience) Brief(ctx context.Context, id string) error {
	_, err := a.b.GetBrief(ctx, id)
	return err
}

func (a announceAudience) BriefAgents(ctx context.Context, briefID string) ([]string, error) {
	return a.b.ActiveAgentsOfBrief(ctx, briefID)
}

func (a announceAudience) AllAgents(ctx context.Context) ([]string, error) {
	return liveAgents(ctx, a.b, "")
}

func liveAgents(ctx context.Context, b *board.Board, role string) ([]string, error) {
	as, err := b.ListAgents(ctx, role, "")
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(as))
	for _, a := range as {
		if a.Status != model.AgentRevoked {
			ids = append(ids, a.ID)
		}
	}
	return ids, nil
}

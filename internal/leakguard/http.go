package leakguard

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/tokens"
)

type Reissued struct {
	LeakID  string         `json:"leak_id"`
	Kind    string         `json:"kind"`
	Agent   *model.Agent   `json:"agent,omitempty"`
	Connect *board.Connect `json:"connect,omitempty"`
	Token   *tokens.Token  `json:"token,omitempty"`
	Secret  string         `json:"secret,omitempty"`
}

func (g *Guard) Reissue(ctx context.Context, id string) (*Reissued, error) {
	l, resolved, err := g.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if l == nil {
		return nil, model.NotFound("token leak %s not found", id)
	}
	if resolved {
		return nil, model.Conflict("token leak %s is already resolved", id)
	}
	if l.Kind == KindAgent {
		r, err := g.o.Board.ReissueLeaked(ctx, l.Subject, func(tx store.Tx) error { return g.resolve(tx, id) })
		if err != nil {
			return nil, err
		}
		return &Reissued{LeakID: id, Kind: KindAgent, Agent: r.Agent, Connect: &r.Connect}, nil
	}
	return g.reissueAPIToken(ctx, l)
}

func (g *Guard) reissueAPIToken(ctx context.Context, l *leak) (*Reissued, error) {
	if g.o.Tokens == nil {
		return nil, model.Conflict("api tokens are not available")
	}
	all, err := g.o.Tokens.ListAPITokens(ctx)
	if err != nil {
		return nil, err
	}
	var old *tokens.Token
	for _, t := range all {
		if t.ID == l.Subject {
			old = t
		}
	}
	if old == nil {
		return nil, model.NotFound("api token %s not found", l.Subject)
	}
	now := g.o.Now()
	err = g.o.Store.Atomic(ctx, func(tx store.Tx) error {
		if err := g.resolve(tx, l.ID); err != nil {
			return err
		}
		note := auth.AuditNote(ctx, "")
		return tx.InsertAudit(&model.AuditEvent{
			TS: now, AgentID: model.Owner.ID, Role: model.RoleOwner, Tool: ReissueTool, Note: &note,
			Before: map[string]any{"token_id": old.ID},
			After:  map[string]any{"leak_id": l.ID, "token_id": old.ID},
		})
	})
	if err != nil {
		return nil, err
	}
	in := tokens.CreateInput{Name: old.Name, Scope: old.Scope}
	if old.ExpiresAt != nil && old.ExpiresAt.After(now) {
		in.ExpiresAt = old.ExpiresAt
	}
	t, secret, err := tokens.New(g.o.Tokens, g.o.Now).Create(ctx, in)
	if err != nil {
		g.reopen(context.WithoutCancel(ctx), l.ID)
		return nil, err
	}
	return &Reissued{LeakID: l.ID, Kind: KindAPIToken, Token: t, Secret: secret}, nil
}

func Handler(g *Guard) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token-leaks/{id}/reissue", func(w http.ResponseWriter, r *http.Request) {
		out, err := g.Reissue(r.Context(), r.PathValue("id"))
		if err != nil {
			e := model.AsError(err)
			if e.Code == "internal" {
				g.o.Log.Error("token leak reissue", "leak", r.PathValue("id"), "err", err)
			}
			writeJSON(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

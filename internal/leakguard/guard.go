package leakguard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/tokens"
)

const (
	KindAgent    = "agent"
	KindAPIToken = "api_token"

	Tool        = "token_leak_detected"
	ReissueTool = "reissue_leaked_token"
	Topic       = "alerts"
	priority    = 5
)

var system = model.Actor{ID: "system", Role: "system"}

type Options struct {
	Board  *board.Board
	Store  store.Store
	Agents auth.AgentLookup
	Tokens tokens.Store
	Pub    events.Publisher
	Log    *slog.Logger
	Now    func() time.Time
}

type Guard struct {
	o Options
}

func New(o Options) *Guard {
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Guard{o: o}
}

func (g *Guard) Scan(ctx context.Context, src board.LeakSource, text string) string {
	found := candidates(text)
	if len(found) == 0 {
		return text
	}
	ctx = context.WithoutCancel(ctx)
	var real []*candidate
	for _, c := range found {
		if g.respond(ctx, src, c.value) {
			real = append(real, c)
		}
	}
	if len(real) == 0 {
		return text
	}
	return mask(text, real)
}

func (g *Guard) respond(ctx context.Context, src board.LeakSource, value string) bool {
	switch value[:len(auth.TokenPrefix)] {
	case auth.TokenPrefix:
		return g.agentLeak(ctx, src, value)
	case auth.OwnerTokenPrefix:
		return g.apiTokenLeak(ctx, src, value)
	}
	return false
}

func (g *Guard) agentLeak(ctx context.Context, src board.LeakSource, value string) bool {
	a, err := g.o.Agents.LookupToken(value)
	if err != nil {
		if !errors.Is(err, auth.ErrTokenExpired) {
			g.o.Log.Error("token leak lookup failed", "channel", src.Channel, "err", err)
		}
		return false
	}
	if a == nil {
		return false
	}
	id, err := g.o.Board.RevokeLeaked(ctx, a.ID, src, func(tx store.Tx) (string, error) {
		return g.record(tx, KindAgent, a.ID, src)
	})
	if err != nil {
		g.o.Log.Error("token leak response failed", "agent", a.ID, "channel", src.Channel, "err", err)
		return true
	}
	if id != "" {
		g.notify(id, fmt.Sprintf("Token of agent %s leaked", a.ID), src)
	}
	return true
}

func (g *Guard) apiTokenLeak(ctx context.Context, src board.LeakSource, value string) bool {
	if g.o.Tokens == nil {
		return false
	}
	t, err := g.o.Tokens.APITokenByHash(ctx, auth.Hash(value))
	if err != nil {
		g.o.Log.Error("token leak lookup failed", "channel", src.Channel, "err", err)
		return false
	}
	if t == nil || t.RevokedAt != nil {
		return false
	}
	now := g.o.Now()
	_, err = g.o.Tokens.RevokeAPIToken(ctx, t.ID, now, func(rt *tokens.Token) *model.AuditEvent {
		note := "author=" + src.Actor.ID
		return &model.AuditEvent{
			TS: now, AgentID: system.ID, Role: system.Role, Tool: Tool,
			Before: map[string]any{"token_id": rt.ID, "revoked": false},
			After:  map[string]any{"identity": KindAPIToken, "token_id": rt.ID, "revoked": true, "channel": src.Channel},
			Note:   &note,
		}
	})
	switch {
	case errors.Is(err, tokens.ErrRevoked):
		return true
	case err != nil:
		g.o.Log.Error("token leak response failed", "token", t.ID, "channel", src.Channel, "err", err)
		return true
	}
	var id string
	err = g.o.Store.Atomic(ctx, func(tx store.Tx) (e error) {
		id, e = g.record(tx, KindAPIToken, t.ID, src)
		return
	})
	if err != nil {
		g.o.Log.Error("token leak record failed", "token", t.ID, "channel", src.Channel, "err", err)
		return true
	}
	if err := g.o.Board.NoteLeakedAPIToken(ctx, src, t.ID); err != nil {
		g.o.Log.Error("token leak notice failed", "token", t.ID, "err", err)
	}
	g.notify(id, fmt.Sprintf("API token %s leaked", t.ID), src)
	return true
}

func (g *Guard) notify(id, title string, src board.LeakSource) {
	if g.o.Pub == nil {
		return
	}
	g.o.Pub.Publish([]string{Topic}, "alert.raised", notify.Notification{
		ID: "token_leak:" + id, Kind: "token_leak", Title: title, Priority: priority, TS: g.o.Now(),
		Text: fmt.Sprintf("Found in a %s by %s and revoked. Re-issue it: POST /api/v1/token-leaks/%s/reissue", src.Channel, src.Actor.ID, id),
	})
}

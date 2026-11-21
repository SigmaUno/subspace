package board

import (
	"context"
	"fmt"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	ChannelMessage  = "message"
	ChannelNote     = "note"
	ChannelQuestion = "question"
	ChannelSummary  = "submit_summary"
	ChannelStatus   = "status_note"
	ChannelTests    = "test_log"
	ChannelReview   = "review"
	ChannelPR       = "pr_text"
	ChannelOutput   = "log_output"
	ChannelTask     = "task_text"
)

type LeakSource struct {
	Channel string
	Actor   model.Actor
}

type LeakScanner func(ctx context.Context, src LeakSource, text string) string

func (b *Board) SetLeakScanner(f LeakScanner) {
	b.mu.Lock()
	b.leak = f
	b.mu.Unlock()
}

func (b *Board) Scan(ctx context.Context, a model.Actor, channel, text string) string {
	if text == "" {
		return text
	}
	b.mu.Lock()
	f := b.leak
	b.mu.Unlock()
	if f == nil {
		return text
	}
	return f(ctx, LeakSource{Channel: channel, Actor: a}, text)
}

func (b *Board) scrub(ctx context.Context, a model.Actor, channel, text string) string {
	return Redact(b.Scan(ctx, a, channel, text))
}

func (b *Board) scrubAll(ctx context.Context, a model.Actor, channel string, xs []string) []string {
	if xs == nil {
		return nil
	}
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = b.scrub(ctx, a, channel, x)
	}
	return out
}

func (b *Board) scrubTask(ctx context.Context, a model.Actor, in TaskInput) TaskInput {
	in.Title = b.scrub(ctx, a, ChannelTask, in.Title)
	in.Spec = b.scrub(ctx, a, ChannelTask, in.Spec)
	in.AcceptanceCriteria = b.scrubAll(ctx, a, ChannelTask, in.AcceptanceCriteria)
	return in
}

func messageChannel(kind string) string {
	switch kind {
	case model.MsgNote:
		return ChannelNote
	case model.MsgQuestion:
		return ChannelQuestion
	}
	return ChannelMessage
}

func (b *Board) RevokeLeaked(ctx context.Context, id, tokenHash string, src LeakSource, record func(tx store.Tx) (string, error)) (string, error) {
	sys := model.Actor{ID: "system", Role: "system"}
	var leakID string
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		leakID = ""
		a, err := tx.GetAgent(id)
		if err != nil {
			return model.NotFound("agent %s not found", id)
		}
		if a.Status == model.AgentRevoked || a.TokenHash != tokenHash {
			return nil
		}
		before := a.Status
		a.Status, a.PauseReason = model.AgentRevoked, strp("token leaked in "+src.Channel)
		if err := b.releaseAgentTasks(tx, w, id, "agent token leaked"); err != nil {
			return err
		}
		if err := tx.UpdateAgent(a); err != nil {
			return err
		}
		w.agent(a)
		if leakID, err = record(tx); err != nil {
			return err
		}
		body := fmt.Sprintf("The token of agent %s was found in a %s and revoked. The owner has been asked to re-issue it.", id, src.Channel)
		if err := w.post(tx, model.ThreadAgent, id, sys, model.MsgSystem, body); err != nil {
			return err
		}
		if src.Actor.ID != id {
			if _, err := tx.GetAgent(src.Actor.ID); err == nil {
				note := fmt.Sprintf("A token of agent %s appeared in your %s and was revoked. Never put tokens in text you send.", id, src.Channel)
				if err := w.post(tx, model.ThreadAgent, src.Actor.ID, sys, model.MsgSystem, note); err != nil {
					return err
				}
			}
		}
		return w.audit(tx, sys, "token_leak_detected", "",
			map[string]any{"agent": id, "status": before},
			map[string]any{"identity": "agent", "status": model.AgentRevoked, "channel": src.Channel, "leak_id": leakID},
			"author="+src.Actor.ID)
	})
	return leakID, err
}

func (b *Board) NoteLeakedAPIToken(ctx context.Context, src LeakSource, tokenID string) error {
	sys := model.Actor{ID: "system", Role: "system"}
	return b.write(ctx, func(tx store.Tx, w *wctx) error {
		if _, err := tx.GetAgent(src.Actor.ID); err != nil {
			return nil
		}
		body := fmt.Sprintf("API token %s appeared in your %s and was revoked. Never put tokens in text you send.", tokenID, src.Channel)
		return w.post(tx, model.ThreadAgent, src.Actor.ID, sys, model.MsgSystem, body)
	})
}

func (b *Board) ReissueLeaked(ctx context.Context, id string, mark func(tx store.Tx) error) (*Registered, error) {
	var tok string
	a, err := b.setAgent(ctx, model.Owner, "reissue_leaked_token", id, func(tx store.Tx, w *wctx, a *model.Agent) error {
		if a.Status != model.AgentRevoked {
			return model.Conflict("agent %s is not revoked", id)
		}
		a.Status, a.PauseReason = model.AgentActive, nil
		if a.TokenExpiresAt != nil && !a.TokenExpiresAt.After(b.now()) {
			a.TokenExpiresAt = nil
		}
		var err error
		if tok, err = auth.NewToken(); err != nil {
			return err
		}
		a.TokenHash = auth.Hash(tok)
		return mark(tx)
	})
	if err != nil {
		return nil, err
	}
	return &Registered{Agent: a, Connect: b.ConnectInfo(id, tok)}, nil
}

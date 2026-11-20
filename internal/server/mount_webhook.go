package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/webhook"
)

var webhookSystem = model.Actor{ID: "system", Role: "system"}

type prSync struct {
	b   *board.Board
	log *slog.Logger
}

func init() {
	registerMount(func(d Deps) {
		ps := &prSync{b: d.Board, log: d.Log}
		for _, h := range []struct{ provider, env, path string }{
			{webhook.ProviderGitHub, "SUBSPACE_WEBHOOK_SECRET_GITHUB", "/hooks/github"},
			{webhook.ProviderForgejo, "SUBSPACE_WEBHOOK_SECRET_FORGEJO", "/hooks/forgejo"},
		} {
			secret := os.Getenv(h.env)
			if secret == "" {
				continue
			}
			handler, err := webhook.NewProviderHandler(h.provider, secret, ps.handle)
			if err != nil {
				d.Log.Error("webhook disabled", "provider", h.provider, "err", err)
				continue
			}
			d.Mux.Handle(h.path, handler)
		}
	})
}

func (s *prSync) handle(ctx context.Context, ev webhook.Event) error {
	tasks, err := s.b.ListTasks(ctx, board.TaskQuery{Limit: 100000})
	if err != nil {
		return err
	}
	var errs []error
	for _, t := range tasks {
		if t.PRURL != nil && strings.EqualFold(strings.TrimRight(*t.PRURL, "/"), strings.TrimRight(ev.URL, "/")) {
			errs = append(errs, s.apply(ctx, t, ev))
		}
	}
	return errors.Join(errs...)
}

func (s *prSync) apply(ctx context.Context, t *model.Task, ev webhook.Event) error {
	switch {
	case ev.Closed:
		return s.post(ctx, t, model.MsgSystem, fmt.Sprintf("Warning: pull request #%d was closed without merging: %s", ev.Number, ev.URL))
	case ev.Merged:
		return s.merged(ctx, t, ev)
	default:
		return s.post(ctx, t, model.MsgNote, fmt.Sprintf("Pull request #%d opened or updated on %s: %s", ev.Number, ev.HeadBranch, ev.URL))
	}
}

func (s *prSync) merged(ctx context.Context, t *model.Task, ev webhook.Event) error {
	br, err := s.b.GetBrief(ctx, t.BriefID)
	if err != nil {
		return err
	}
	note := fmt.Sprintf("Pull request #%d merged: %s", ev.Number, ev.URL)
	if !br.AutoDoneOnMerge {
		return s.post(ctx, t, model.MsgNote, note)
	}
	_, err = s.b.MarkDone(ctx, t.ID, note)
	if model.AsError(err).Code == "conflict" {
		return s.post(ctx, t, model.MsgNote, fmt.Sprintf("%s. Task was not marked done: %s", note, err))
	}
	return err
}

func (s *prSync) post(ctx context.Context, t *model.Task, kind, body string) error {
	_, err := s.b.AppendMessage(ctx, model.ThreadTask, t.ID, model.Message{AuthorID: webhookSystem.ID, AuthorRole: webhookSystem.Role, Kind: kind, Body: body})
	return err
}

package server

import (
	"context"
	"fmt"
	"os"

	"github.com/SigmaUno/subspace/internal/reviewsync"
)

type reviewCommentStore interface {
	ReviewComment(ctx context.Context, taskID string) (prURL string, commentID, messageID int64, digest string, ok bool, err error)
	SetReviewComment(ctx context.Context, taskID, prURL string, commentID, messageID int64, digest string) error
}

type reviewComments struct{ s reviewCommentStore }

func (c reviewComments) ReviewComment(ctx context.Context, taskID string) (reviewsync.Record, bool, error) {
	prURL, id, msg, digest, ok, err := c.s.ReviewComment(ctx, taskID)
	return reviewsync.Record{PRURL: prURL, CommentID: id, MessageID: msg, Digest: digest}, ok, err
}

func (c reviewComments) SetReviewComment(ctx context.Context, taskID string, r reviewsync.Record) error {
	return c.s.SetReviewComment(ctx, taskID, r.PRURL, r.CommentID, r.MessageID, r.Digest)
}

func init() { registerMount(mountReviewSync) }

func mountReviewSync(d Deps) {
	mode, on, err := reviewsync.ParseMode(os.Getenv("SUBSPACE_REVIEWSYNC"))
	if err != nil {
		d.Log.Error("review comment sync disabled", "err", err)
		return
	}
	if !on {
		return
	}
	st, ok := d.Store.(reviewCommentStore)
	if !ok {
		d.Log.Error("review comment sync disabled", "err", "store does not keep review comments")
		return
	}
	d.Leader.Go(d.Ctx, func(ctx context.Context) {
		superviseRepos(ctx, d, func(ctx context.Context) (func(context.Context), error) {
			cfg, ok, err := resolveRepos(ctx, d, "review comment sync")
			if err != nil {
				return nil, err
			}
			if !ok {
				d.Log.Warn("review comment sync waiting for repositories: none are registered and SUBSPACE_PRSYNC_CONFIG is not set")
				return nil, nil
			}
			svc, err := reviewsync.New(reviewsync.Options{
				Tasks: d.Board, Store: reviewComments{st}, Hosts: reviewsync.HostsFromConfig(cfg, nil), Repos: cfg.Repos,
				PublicURL: d.Cfg.PublicURL, Mode: mode, Log: d.Log,
			})
			if err != nil {
				return nil, fmt.Errorf("review comment sync disabled: %w", err)
			}
			return func(ctx context.Context) {
				svc.Start(ctx, d.Leader.Gate(d.Hub))
				<-ctx.Done()
			}, nil
		})
	})
}

package server

import (
	"context"
	"os"

	"github.com/SigmaUno/subspace/internal/prsync"
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
	cfg, ok, err := prsync.Resolve(d.Ctx, repoSource(d), d.Log)
	if err != nil {
		d.Log.Error("review comment sync disabled", "err", err)
		return
	}
	if !ok {
		d.Log.Error("review comment sync disabled", "err", "no repositories are registered and SUBSPACE_PRSYNC_CONFIG is not set")
		return
	}
	svc, err := reviewsync.New(reviewsync.Options{
		Tasks: d.Board, Store: reviewComments{st}, Hosts: reviewsync.HostsFromConfig(cfg, nil), Repos: cfg.Repos,
		PublicURL: d.Cfg.PublicURL, Mode: mode, Log: d.Log,
	})
	if err != nil {
		d.Log.Error("review comment sync disabled", "err", err)
		return
	}
	svc.Start(d.Ctx, d.Hub)
}

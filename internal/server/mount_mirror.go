package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/SigmaUno/subspace/internal/mirror"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	mirrorEnvURL     = "SUBSPACE_MIRROR_FORGEJO_URL"
	mirrorEnvToken   = "SUBSPACE_MIRROR_FORGEJO_TOKEN"
	mirrorEnvRepoMap = "SUBSPACE_MIRROR_FORGEJO_REPO_MAP"
)

type mirrorLinkStore interface {
	MirrorLink(ctx context.Context, taskID string) (repo, url, digest string, ok bool, err error)
	SetMirrorLink(ctx context.Context, taskID, repo, url, digest string) error
}

type mirrorLinks struct{ s mirrorLinkStore }

func (l mirrorLinks) Get(ctx context.Context, taskID string) (mirror.Link, bool, error) {
	repo, url, digest, ok, err := l.s.MirrorLink(ctx, taskID)
	return mirror.Link{Repo: repo, URL: url, Digest: digest}, ok, err
}

func (l mirrorLinks) Put(ctx context.Context, taskID string, k mirror.Link) error {
	return l.s.SetMirrorLink(ctx, taskID, k.Repo, k.URL, k.Digest)
}

func init() {
	registerMount(func(d Deps) {
		base, token, rawMap := os.Getenv(mirrorEnvURL), os.Getenv(mirrorEnvToken), os.Getenv(mirrorEnvRepoMap)
		if base == "" && token == "" && rawMap == "" {
			return
		}
		links, ok := d.Store.(mirrorLinkStore)
		if !ok {
			d.Log.Error("mirror disabled", "err", "store does not support issue links")
			return
		}
		mirrors, err := forgejoMirrors(base, token, rawMap)
		if err != nil {
			d.Log.Error("mirror disabled", "err", err)
			return
		}
		syncer := mirror.NewSyncer(mirror.SyncConfig{
			Mirrors: mirrors,
			Links:   mirrorLinks{links},
			BriefTitle: func(ctx context.Context, id string) string {
				var title string
				_ = d.Store.View(ctx, func(tx store.Tx) error {
					b, err := tx.GetBrief(id)
					if err == nil {
						title = b.Title
					}
					return nil
				})
				return title
			},
			Log: d.Log,
		})
		syncer.Start(context.Background(), d.Hub)
	})
}

func forgejoMirrors(base, token, rawMap string) (map[string]mirror.IssueMirror, error) {
	if base == "" || token == "" || rawMap == "" {
		return nil, fmt.Errorf("%s, %s and %s must all be set", mirrorEnvURL, mirrorEnvToken, mirrorEnvRepoMap)
	}
	out := map[string]mirror.IssueMirror{}
	for _, entry := range strings.Split(rawMap, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		src, dst, ok := strings.Cut(entry, "=")
		owner, repo, ok2 := strings.Cut(strings.TrimSpace(dst), "/")
		src = strings.TrimSpace(src)
		if !ok || !ok2 || src == "" || owner == "" || repo == "" || strings.Contains(repo, "/") {
			return nil, fmt.Errorf("%s: entry %q must look like task-repo=owner/repo", mirrorEnvRepoMap, entry)
		}
		m, err := mirror.NewForgejo(mirror.ForgejoConfig{BaseURL: base, Token: token, Owner: owner, Repo: repo})
		if err != nil {
			return nil, errors.New(strings.ReplaceAll(err.Error(), token, "***"))
		}
		out[src] = m
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s has no entries", mirrorEnvRepoMap)
	}
	return out, nil
}

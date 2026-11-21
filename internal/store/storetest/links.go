package storetest

import (
	"context"
	"testing"

	"github.com/SigmaUno/subspace/internal/store"
)

type prStore interface {
	PRStatus(ctx context.Context, taskID string) ([]byte, bool, error)
	PRStatuses(ctx context.Context) ([][]byte, error)
	SetPRStatus(ctx context.Context, taskID string, data []byte) error
}

type mirrorStore interface {
	MirrorLink(ctx context.Context, taskID string) (repo, url, digest string, ok bool, err error)
	SetMirrorLink(ctx context.Context, taskID, repo, url, digest string) error
}

type reviewStore interface {
	ReviewComment(ctx context.Context, taskID string) (prURL string, commentID, messageID int64, digest string, ok bool, err error)
	SetReviewComment(ctx context.Context, taskID, prURL string, commentID, messageID int64, digest string) error
}

func scenarioLinks(t *testing.T, s store.Store) {
	ctx := context.Background()

	pr, ok := s.(prStore)
	if !ok {
		t.Fatal("store does not keep PR snapshots")
	}
	if _, found, err := pr.PRStatus(ctx, "T-1"); err != nil || found {
		t.Fatalf("empty PRStatus: %v %v", found, err)
	}
	for id, data := range map[string]string{"T-10": `{"n":10}`, "T-2": `{"n":2}`, "T-1": `{"n":1}`} {
		if err := pr.SetPRStatus(ctx, id, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := pr.SetPRStatus(ctx, "T-1", []byte(`{"n":"1b","q":"it's \"quoted\" ü"}`)); err != nil {
		t.Fatal(err)
	}
	got, found, err := pr.PRStatus(ctx, "T-1")
	if err != nil || !found || string(got) != `{"n":"1b","q":"it's \"quoted\" ü"}` {
		t.Fatalf("PRStatus: %s %v %v", got, found, err)
	}
	all, err := pr.PRStatuses(ctx)
	if err != nil || len(all) != 3 || string(all[1]) != `{"n":2}` || string(all[2]) != `{"n":10}` {
		t.Fatalf("PRStatuses by task number: %q %v", all, err)
	}

	m, ok := s.(mirrorStore)
	if !ok {
		t.Fatal("store does not keep mirror links")
	}
	if _, _, _, found, err := m.MirrorLink(ctx, "T-1"); err != nil || found {
		t.Fatalf("empty MirrorLink: %v %v", found, err)
	}
	for _, digest := range []string{"d1", "d2"} {
		if err := m.SetMirrorLink(ctx, "T-1", "o/r", "https://forge/issues/1", digest); err != nil {
			t.Fatal(err)
		}
	}
	if repo, url, digest, found, err := m.MirrorLink(ctx, "T-1"); err != nil || !found || repo != "o/r" || url != "https://forge/issues/1" || digest != "d2" {
		t.Fatalf("MirrorLink: %s %s %s %v %v", repo, url, digest, found, err)
	}

	r, ok := s.(reviewStore)
	if !ok {
		t.Fatal("store does not keep review comments")
	}
	if _, _, _, _, found, err := r.ReviewComment(ctx, "T-1"); err != nil || found {
		t.Fatalf("empty ReviewComment: %v %v", found, err)
	}
	const big = int64(1) << 40
	for _, c := range []struct {
		pr     string
		id, ms int64
		digest string
	}{{"https://h/pull/1", 7, 3, "a"}, {"https://h/pull/2", big, big + 1, "b"}} {
		if err := r.SetReviewComment(ctx, "T-1", c.pr, c.id, c.ms, c.digest); err != nil {
			t.Fatal(err)
		}
	}
	pr1, id, msg, digest, found, err := r.ReviewComment(ctx, "T-1")
	if err != nil || !found || pr1 != "https://h/pull/2" || id != big || msg != big+1 || digest != "b" {
		t.Fatalf("ReviewComment: %s %d %d %s %v %v", pr1, id, msg, digest, found, err)
	}
}

package replay

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

func openBriefs(t *testing.T, n int) (*Service, []string) {
	t.Helper()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "replay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ids := make([]string, n)
	err = st.Atomic(context.Background(), func(tx store.Tx) error {
		for i := range ids {
			ids[i] = fmt.Sprintf("B-%d", i+1)
			b := &model.Brief{ID: ids[i], Title: "t", Status: "open", PlanStatus: "none", CreatedAt: time.Unix(int64(i), 0).UTC()}
			if err := tx.InsertBrief(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewService(st), ids
}

func TestServiceEvictsLeastRecentlyUsedBrief(t *testing.T) {
	ctx := context.Background()
	s, ids := openBriefs(t, maxCached+2)

	for _, id := range ids[:maxCached] {
		if _, err := s.Timeline(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.briefs) != maxCached {
		t.Fatalf("cached %d histories, want %d", len(s.briefs), maxCached)
	}

	if _, err := s.Timeline(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Timeline(ctx, ids[maxCached]); err != nil {
		t.Fatal(err)
	}
	if len(s.briefs) != maxCached || len(s.used) != maxCached {
		t.Fatalf("cache holds %d histories and %d recency entries, want %d", len(s.briefs), len(s.used), maxCached)
	}
	if _, ok := s.briefs[ids[0]]; !ok {
		t.Fatalf("recently used %s was evicted", ids[0])
	}
	if _, ok := s.briefs[ids[1]]; ok {
		t.Fatalf("least recently used %s was kept", ids[1])
	}

	tl, err := s.Timeline(ctx, ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if tl.BriefID != ids[1] {
		t.Fatalf("timeline of evicted brief rebuilt for %q, want %q", tl.BriefID, ids[1])
	}
}

func TestServiceRejectsUnknownBriefAndBadPosition(t *testing.T) {
	ctx := context.Background()
	s, ids := openBriefs(t, 1)

	_, err := s.Snapshot(ctx, "B-404", "")
	var me *model.Error
	if !errors.As(err, &me) || me.Code != "not_found" {
		t.Fatalf("unknown brief: %v", err)
	}
	if len(s.briefs) != 0 {
		t.Fatalf("failed lookup cached %d histories", len(s.briefs))
	}

	for _, at := range []string{"yesterday", "-1"} {
		_, err = s.Snapshot(ctx, ids[0], at)
		if !errors.As(err, &me) || me.Code != "bad_request" {
			t.Fatalf("at %q: %v", at, err)
		}
	}
	if _, err = s.Diff(ctx, ids[0], "", "later"); !errors.As(err, &me) || me.Code != "bad_request" {
		t.Fatalf("diff to: %v", err)
	}
}

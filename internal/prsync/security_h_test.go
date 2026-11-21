package prsync_test

import (
	"testing"

	"github.com/SigmaUno/subspace/internal/prsync"
)

func TestPullRequestMustBelongToTaskRepoAndHost(t *testing.T) {
	gh, fj := newFakeHost(t, prsync.KindGitHub), newFakeHost(t, prsync.KindForgejo)
	h := newHarness(t, gh, fj, "", `{"per_minute": 6000, "burst": 1000}`)
	ids := h.setup(2)
	if code, out := h.e.REST("PATCH", "/api/v1/briefs/"+h.bid, map[string]any{"auto_done_on_merge": true}); code != 200 {
		t.Fatalf("patch brief: %d %v", code, out)
	}
	gh.set("o/other", 1, func(p *fakePR) { p.Merged = true })
	gh.set("o/gh", 2, func(p *fakePR) { p.Merged = true })
	urls := []string{"https://github.com/o/other/pull/1", "https://evil.example/o/gh/pull/2"}
	for i, id := range ids {
		h.submit(id, urls[i])
		h.e.Must(h.rev, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": "ok"})
	}
	h.sync()
	if n, _ := gh.counts(); n != 0 {
		t.Fatalf("fetched %d times for foreign pull requests", n)
	}
	for _, id := range ids {
		if _, task := h.e.REST("GET", "/api/v1/tasks/"+id, nil); task["status"] != "approved" {
			t.Fatalf("%s: status %v after foreign merge", id, task["status"])
		}
		if s := h.status(id); s["error"] == nil || s["state"] != "" {
			t.Fatalf("%s: status %v", id, s)
		}
	}
}

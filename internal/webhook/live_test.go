package webhook_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/forgejotest"
	"github.com/SigmaUno/subspace/internal/forgejotest/serverrig"
	"github.com/SigmaUno/subspace/internal/webhook"
)

type collector struct {
	mu     sync.Mutex
	events []webhook.Event
}

func (c *collector) fn(_ context.Context, ev webhook.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
	return nil
}

func (c *collector) wait(t *testing.T, n int) []webhook.Event {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		c.mu.Lock()
		got := append([]webhook.Event(nil), c.events...)
		c.mu.Unlock()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("got %d webhook events, want %d: %+v", len(got), n, got)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestLiveForgejoDeliveries(t *testing.T) {
	inst := forgejotest.Start(t)
	const secret = "live-hook-secret-0123456789"
	col := &collector{}
	h, err := webhook.NewProviderHandler(webhook.ProviderForgejo, secret, col.fn)
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu       sync.Mutex
		statuses []int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		mu.Lock()
		statuses = append(statuses, rec.Code)
		mu.Unlock()
		w.WriteHeader(rec.Code)
	}))
	defer srv.Close()

	repo := inst.NewRepo(t)
	repo.AddHook(t, srv.URL+"/hooks/forgejo", secret, "pull_request", "pull_request_sync")
	repo.Commit(t, "feat-a", "main", "a.txt", "a\n")
	pr := repo.OpenPR(t, "feat-a", "main", "Add a")
	col.wait(t, 1)
	repo.Commit(t, "feat-a", "main", "b.txt", "b\n")
	col.wait(t, 2)
	repo.Merge(t, pr.Number)

	base := webhook.Event{Provider: webhook.ProviderForgejo, Repo: repo.FullName(), Number: int(pr.Number), HeadBranch: "feat-a", URL: repo.PRURL(pr.Number)}
	merged := base
	merged.Merged = true
	got := map[webhook.Event]int{}
	for _, ev := range col.wait(t, 3) {
		got[ev]++
	}
	if got[base] != 2 || got[merged] != 1 || len(got) != 2 {
		t.Fatalf("events = %+v, want 2 x %+v and 1 x %+v", got, base, merged)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, code := range statuses {
		if code != http.StatusOK {
			t.Errorf("receiver answered %v", statuses)
			break
		}
	}
}

func TestLiveForgejoRejectsWrongSecret(t *testing.T) {
	inst := forgejotest.Start(t)
	col := &collector{}
	h, err := webhook.NewProviderHandler(webhook.ProviderForgejo, "the-receiver-secret-0123456789", col.fn)
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu    sync.Mutex
		codes []int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		mu.Lock()
		codes = append(codes, rec.Code)
		mu.Unlock()
		w.WriteHeader(rec.Code)
	}))
	defer srv.Close()

	repo := inst.NewRepo(t)
	repo.AddHook(t, srv.URL, "another-secret-0123456789", "pull_request")
	repo.Commit(t, "feat-a", "main", "a.txt", "a\n")
	repo.OpenPR(t, "feat-a", "main", "Add a")

	deadline := time.Now().Add(20 * time.Second)
	for {
		mu.Lock()
		n := len(codes)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no delivery arrived")
		}
		time.Sleep(100 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if codes[0] != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", codes[0])
	}
	col.mu.Lock()
	defer col.mu.Unlock()
	if len(col.events) != 0 {
		t.Fatalf("events accepted with a wrong secret: %+v", col.events)
	}
}

func TestLiveForgejoMergeMarksTaskDoneThroughTheServer(t *testing.T) {
	inst := forgejotest.Start(t)
	const secret = "live-server-hook-secret-0123456789"
	repo := inst.NewRepo(t)
	repo.Commit(t, "sub/t-1-a", "main", "a.txt", "a\n")
	pr := repo.OpenPR(t, "sub/t-1-a", "main", "Add a")
	rig := serverrig.New(t, inst, repo, map[string]string{"SUBSPACE_WEBHOOK_SECRET_FORGEJO": secret})
	repo.AddHook(t, rig.E.TS.URL+"/hooks/forgejo", secret, "pull_request", "pull_request_sync")
	if code, out := rig.E.REST("PATCH", "/api/v1/briefs/"+rig.Brief, map[string]any{"auto_done_on_merge": true}); code != 200 {
		t.Fatalf("patch brief: %d %v", code, out)
	}

	id := rig.Task("Add a")
	rig.Submit(id, pr)
	rig.Review(id, "approve", "ok")
	repo.Merge(t, pr.Number)

	deadline := time.Now().Add(20 * time.Second)
	for rig.Get(id)["status"] != "done" {
		if time.Now().After(deadline) {
			t.Fatalf("task = %v", rig.Get(id))
		}
		time.Sleep(100 * time.Millisecond)
	}
	merged := false
	for _, m := range rig.Messages(id) {
		if strings.Contains(m["body"].(string), "Pull request #1 merged: "+repo.PRURL(pr.Number)) {
			merged = true
		}
	}
	if !merged {
		t.Fatalf("no merge note in %v", rig.Messages(id))
	}
}

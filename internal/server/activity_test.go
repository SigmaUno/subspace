package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/activity"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type activityPage struct {
	Items      []activity.Item `json:"items"`
	NextCursor *string         `json:"next_cursor"`
}

func getActivity(t *testing.T, e *servertest.Env, q url.Values) activityPage {
	t.Helper()
	req, _ := http.NewRequest("GET", e.TS.URL+"/api/v1/activity?"+q.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("activity %v: %d", q, resp.StatusCode)
	}
	var p activityPage
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

func texts(items []activity.Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Text
	}
	return out
}

func wantText(t *testing.T, items []activity.Item, want string) activity.Item {
	t.Helper()
	for _, it := range items {
		if it.Text == want {
			return it
		}
	}
	t.Fatalf("no activity %q in:\n%s", want, strings.Join(texts(items), "\n"))
	return activity.Item{}
}

func collectActivityEvents(t *testing.T, e *servertest.Env) (func() []activity.Item, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", e.TS.URL+"/api/v1/events?topics=activity", nil)
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []activity.Item
	ready := make(chan struct{})
	go func() {
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 1<<20)
		var typ string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "event: hello":
				close(ready)
			case strings.HasPrefix(line, "event: "):
				typ = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: ") && typ == "activity.new":
				var ev struct{ Data activity.Item }
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) == nil {
					mu.Lock()
					got = append(got, ev.Data)
					mu.Unlock()
				}
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("no hello event")
	}
	time.Sleep(700 * time.Millisecond)
	return func() []activity.Item {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}, cancel
}

func TestActivityFeed(t *testing.T) {
	e := servertest.New(t)
	events, stop := collectActivityEvents(t, e)
	defer stop()

	lead := e.Register("rex", "leader", "other")
	wk := e.Register("claude-3", "worker", "claude-code")
	rev := e.Register("codex-r", "reviewer", "codex")

	mkBrief := func() string {
		code, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "body": "b", "repos": []string{"o/r"}, "max_cost_eur": 50, "max_parallel_agents": 10})
		if code != 201 {
			t.Fatalf("brief: %d %v", code, br)
		}
		return br["id"].(string)
	}
	spec := func() map[string]any {
		return map[string]any{"title": "t", "spec": "s", "repo": "o/r", "acceptance_criteria": []string{"ok"}}
	}
	b1, b2 := mkBrief(), mkBrief()
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": b1, "tasks": []any{spec(), spec()}})["plan"].(map[string]any)
	pid := plan["id"].(string)
	if code, out := e.REST("POST", "/api/v1/plans/"+pid+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	tid := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	plan2 := e.Must(lead, "propose_plan", map[string]any{"brief_id": b2, "tasks": []any{spec()}})["plan"].(map[string]any)

	e.Must(lead, "assign", map[string]any{"task_id": tid, "agent_id": "claude-3"})
	e.Must(wk, "claim_task", map[string]any{"task_id": tid})
	e.Must(wk, "submit", map[string]any{"task_id": tid, "pr_url": "https://github.com/o/r/pull/45", "summary": "s"})
	e.Must(rev, "review", map[string]any{"task_id": tid, "verdict": "changes_requested", "comments": "fix it"})

	all := getActivity(t, e, url.Values{})
	for i := 1; i < len(all.Items); i++ {
		if all.Items[i].TS.After(all.Items[i-1].TS) {
			t.Fatalf("items are not newest first at %d", i)
		}
	}
	prop := wantText(t, all.Items, "rex proposed plan "+pid+" with 2 tasks")
	if prop.Icon != "plan" || prop.Severity != "info" || prop.BriefID != b1 || prop.Actor != (activity.Ref{Kind: "agent", ID: "rex"}) {
		t.Errorf("propose item = %+v", prop)
	}
	if !slices.Contains(prop.Targets, activity.Ref{Kind: "plan", ID: pid}) {
		t.Errorf("propose targets = %v", prop.Targets)
	}
	appr := wantText(t, all.Items, "owner approved plan "+pid)
	if appr.Severity != "success" || appr.Actor.Kind != "owner" {
		t.Errorf("approve item = %+v", appr)
	}
	sub := wantText(t, all.Items, "claude-3 submitted "+tid+" (PR #45)")
	if !slices.Contains(sub.Targets, activity.Ref{Kind: "task", ID: tid}) || sub.BriefID != b1 {
		t.Errorf("submit item = %+v", sub)
	}
	rv := wantText(t, all.Items, "codex-r requested changes on "+tid+": fix it")
	if rv.Severity != "warning" {
		t.Errorf("review item = %+v", rv)
	}
	wantText(t, all.Items, "rex proposed plan "+plan2["id"].(string)+" with 1 tasks")

	t.Run("brief filter", func(t *testing.T) {
		got := getActivity(t, e, url.Values{"brief_id": {b2}})
		if len(got.Items) == 0 {
			t.Fatal("no items for brief 2")
		}
		for _, it := range got.Items {
			if it.BriefID != b2 {
				t.Errorf("item %q belongs to %q", it.Text, it.BriefID)
			}
		}
		if len(getActivity(t, e, url.Values{"brief_id": {b1}}).Items) <= len(got.Items) {
			t.Error("brief 1 should have more activity than brief 2")
		}
	})

	t.Run("severity filter", func(t *testing.T) {
		got := getActivity(t, e, url.Values{"severity": {"warning"}})
		if len(got.Items) == 0 {
			t.Fatal("no warnings")
		}
		for _, it := range got.Items {
			if it.Severity != "warning" {
				t.Errorf("item %q has severity %s", it.Text, it.Severity)
			}
		}
	})

	t.Run("cursor pages cover the feed once", func(t *testing.T) {
		var ids []string
		cursor := ""
		for pages := 0; ; pages++ {
			if pages > len(all.Items) {
				t.Fatal("cursor does not terminate")
			}
			q := url.Values{"limit": {"3"}}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			p := getActivity(t, e, q)
			if len(p.Items) > 3 {
				t.Fatalf("page has %d items", len(p.Items))
			}
			for _, it := range p.Items {
				ids = append(ids, it.ID)
			}
			if p.NextCursor == nil {
				break
			}
			cursor = *p.NextCursor
		}
		var want []string
		for _, it := range all.Items {
			want = append(want, it.ID)
		}
		if !slices.Equal(ids, want) {
			t.Errorf("paged ids differ from the full feed\n got: %v\nwant: %v", ids, want)
		}
	})

	t.Run("cursor with filter", func(t *testing.T) {
		var n int
		cursor := ""
		for range 50 {
			q := url.Values{"limit": {"1"}, "brief_id": {b1}}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			p := getActivity(t, e, q)
			n += len(p.Items)
			if p.NextCursor == nil {
				break
			}
			cursor = *p.NextCursor
		}
		if want := len(getActivity(t, e, url.Values{"brief_id": {b1}}).Items); n != want {
			t.Errorf("paged %d items, want %d", n, want)
		}
	})

	t.Run("since", func(t *testing.T) {
		future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		if got := getActivity(t, e, url.Values{"since": {future}}); len(got.Items) != 0 || got.NextCursor != nil {
			t.Errorf("since in the future returned %d items", len(got.Items))
		}
		past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
		if got := getActivity(t, e, url.Values{"since": {past}}); len(got.Items) != len(all.Items) {
			t.Errorf("since in the past returned %d items, want %d", len(got.Items), len(all.Items))
		}
	})

	t.Run("bad requests", func(t *testing.T) {
		for _, q := range []string{"limit=0", "limit=x", "since=yesterday", "severity=loud", "cursor=zzz", "cursor=a0.m0"} {
			code, out := e.REST("GET", "/api/v1/activity?"+q, nil)
			if code != 400 {
				t.Errorf("%s: %d %v", q, code, out)
			}
		}
	})

	t.Run("agents are rejected", func(t *testing.T) {
		req, _ := http.NewRequest("GET", e.TS.URL+"/api/v1/activity", nil)
		req.Header.Set("Authorization", "Bearer "+wk)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("agent token: %d", resp.StatusCode)
		}
	})

	t.Run("sse", func(t *testing.T) {
		var got []activity.Item
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if got = events(); len(got) >= len(all.Items) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		for _, w := range []string{"owner approved plan " + pid, "claude-3 submitted " + tid + " (PR #45)"} {
			wantText(t, got, w)
		}
		seen := map[string]bool{}
		for _, it := range got {
			if seen[it.ID] {
				t.Errorf("event %s delivered twice", it.ID)
			}
			seen[it.ID] = true
		}
	})
}

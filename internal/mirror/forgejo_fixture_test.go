package mirror

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type recorded struct {
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

func (r recorded) write(w http.ResponseWriter) {
	for k, v := range r.Headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(r.Body)
}

type liveFixture struct {
	Labels                 []recorded `json:"labels"`
	SearchRightAfterCreate recorded   `json:"search_right_after_create"`
}

func loadLiveFixture(t *testing.T) liveFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "forgejo_live.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f liveFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRecordedForgejoSearchLagDoesNotDuplicateIssues(t *testing.T) {
	fx := loadLiveFixture(t)
	f := newFakeForgejo(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/repos/o/r/issues") && r.URL.Query().Get("q") != "" {
			fx.SearchRightAfterCreate.write(w)
			return
		}
		f.handle(w, r)
	}))
	t.Cleanup(srv.Close)
	m, err := NewForgejo(ForgejoConfig{BaseURL: srv.URL, Token: "secret", Owner: "o", Repo: "r"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := m.Upsert(ctx, sample())
	if err != nil {
		t.Fatal(err)
	}
	tv := sample()
	tv.Status = "approved"
	second, err := m.Upsert(ctx, tv)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(f.issues) != 1 {
		t.Fatalf("%d issues, urls %q and %q", len(f.issues), first, second)
	}
	if !strings.Contains(strings.Join(f.labelNames(f.issues[0]), ","), "status/approved") {
		t.Fatalf("labels = %v", f.labelNames(f.issues[0]))
	}
}

func TestRecordedForgejoLabelPagesSmallerThanRequested(t *testing.T) {
	fx := loadLiveFixture(t)
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/repos/o/r/labels") {
			posts++
			http.Error(w, "unexpected", http.StatusTeapot)
			return
		}
		n, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if n < 1 || n > len(fx.Labels) {
			recorded{Body: json.RawMessage("[]")}.write(w)
			return
		}
		fx.Labels[n-1].write(w)
	}))
	t.Cleanup(srv.Close)
	m, err := NewForgejo(ForgejoConfig{BaseURL: srv.URL, Token: "secret", Owner: "o", Repo: "r"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"subspace", "status/ready", "filler-0"} {
		if _, err := m.labelID(context.Background(), name, ""); err != nil {
			t.Fatalf("label %s: %v", name, err)
		}
	}
	if posts != 0 || len(m.labels) != 42 {
		t.Fatalf("%d label creations, %d labels known, want 0 and 42", posts, len(m.labels))
	}
}

package forgejo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/SigmaUno/subspace/internal/githost"
)

type recordedCall struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

type mergeFixture struct {
	Repo       string       `json:"repo"`
	Number     int64        `json:"number"`
	SHA        string       `json:"sha"`
	Pushed     recordedCall `json:"pushed"`
	NotAllowed recordedCall `json:"not_allowed"`
	BadSHA     recordedCall `json:"bad_sha"`
	Marked     recordedCall `json:"marked"`
	Merged     recordedCall `json:"merged"`
	Missing    recordedCall `json:"missing"`
}

func loadMergeFixture(t *testing.T) mergeFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "pull_merge.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f mergeFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func (c recordedCall) write(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(c.Status)
	if len(c.Body) > 0 && string(c.Body) != "null" {
		_, _ = w.Write(c.Body)
	}
}

type mergeHost struct {
	f       mergeFixture
	allowed bool

	mu     sync.Mutex
	merged bool
	posts  []string
}

func (h *mergeHost) serve(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	prefix := "/api/v1/repos/" + h.f.Repo + "/pulls/"
	rest, ok := strings.CutPrefix(r.URL.Path, prefix)
	if !ok || r.Header.Get("Authorization") != "token tok" {
		http.NotFound(w, r)
		return
	}
	switch {
	case r.Method == http.MethodGet && rest == "1":
		if h.merged {
			h.f.Merged.write(w)
		} else {
			h.f.Pushed.write(w)
		}
	case r.Method == http.MethodGet:
		h.f.Missing.write(w)
	case r.Method == http.MethodPost && rest == "1/merge":
		var in struct{ Do, MergeCommitID string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		h.posts = append(h.posts, in.Do+" "+in.MergeCommitID)
		switch {
		case !h.allowed:
			h.f.NotAllowed.write(w)
		case in.Do != "manually-merged" || in.MergeCommitID != h.f.SHA:
			h.f.BadSHA.write(w)
		default:
			h.merged = true
			h.f.Marked.write(w)
		}
	default:
		http.NotFound(w, r)
	}
}

func TestRecordedForgejoMarkMerged(t *testing.T) {
	f := loadMergeFixture(t)
	h := &mergeHost{f: f}
	srv := httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(srv.Close)
	c := New(srv.URL, "tok", nil)
	ctx := context.Background()

	pr, err := c.GetPR(ctx, f.Repo, f.Number)
	if err != nil || pr.State != "open" || pr.Merged {
		t.Fatalf("pr after the push to the base branch = %+v %v", pr, err)
	}

	err = c.MarkMerged(ctx, f.Repo, f.Number, f.SHA)
	if err == nil || !strings.Contains(err.Error(), "manually-merged is not allowed") {
		t.Fatalf("repository without manual merge: %v", err)
	}

	h.allowed = true
	if err := c.MarkMerged(ctx, f.Repo, f.Number, "deadbeef"); err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("a commit that is not in the base branch: %v", err)
	}
	if err := c.MarkMerged(ctx, f.Repo, f.Number, f.SHA); err != nil {
		t.Fatal(err)
	}
	if pr, err := c.GetPR(ctx, f.Repo, f.Number); err != nil || !pr.Merged || pr.State != "closed" {
		t.Fatalf("pr after marking = %+v %v", pr, err)
	}
	before := len(h.posts)
	if err := c.MarkMerged(ctx, f.Repo, f.Number, f.SHA); err != nil || len(h.posts) != before {
		t.Fatalf("marking a merged PR again: %v, %d extra requests", err, len(h.posts)-before)
	}
	if err := c.MarkMerged(ctx, f.Repo, 999, f.SHA); !errors.Is(err, githost.ErrNotFound) {
		t.Fatalf("unknown pr: %v", err)
	}
}

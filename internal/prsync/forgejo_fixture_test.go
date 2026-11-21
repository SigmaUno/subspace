package prsync_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

type recorded struct {
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

type snapshot struct {
	Pull    recorded   `json:"pull"`
	Status  recorded   `json:"status"`
	Reviews []recorded `json:"reviews"`
	Files   []recorded `json:"files"`
	Commits []recorded `json:"commits"`
}

type forgejoFixture struct {
	Repo    string `json:"repo"`
	Number  int64  `json:"number"`
	Head    string `json:"head"`
	OldHead string `json:"old_head"`
	NewHead string `json:"new_head"`
	snapshot
	Stale *snapshot `json:"stale"`
	Fresh *snapshot `json:"fresh"`
}

func loadForgejoFixture(t *testing.T, name string) forgejoFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var f forgejoFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func (r recorded) write(w http.ResponseWriter) {
	for k, v := range r.Headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(r.Body)
}

func page(r *http.Request, pages []recorded) recorded {
	n, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if n < 1 || n > len(pages) {
		return recorded{Body: json.RawMessage("[]")}
	}
	return pages[n-1]
}

func replayForgejo(t *testing.T, f forgejoFixture, token string, current func() snapshot) *httptest.Server {
	t.Helper()
	prefix := "/api/v1/repos/" + f.Repo + "/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		snap := current()
		rest, ok := strings.CutPrefix(r.URL.Path, prefix)
		if !ok {
			http.NotFound(w, r)
			return
		}
		n := strconv.FormatInt(f.Number, 10)
		switch {
		case rest == "pulls/"+n:
			snap.Pull.write(w)
		case rest == "pulls/"+n+"/reviews":
			page(r, snap.Reviews).write(w)
		case rest == "pulls/"+n+"/files":
			page(r, snap.Files).write(w)
		case rest == "pulls/"+n+"/commits":
			page(r, snap.Commits).write(w)
		case strings.HasPrefix(rest, "commits/") && strings.HasSuffix(rest, "/status"):
			snap.Status.write(w)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRecordedForgejoPagesSmallerThanRequested(t *testing.T) {
	f := loadForgejoFixture(t, "forgejo_pages.json")
	srv := replayForgejo(t, f, "replay-token", func() snapshot { return f.snapshot })
	r := newForgeRig(t, srv.URL, "replay-token", f.Repo)
	id := r.submit(f.Number)
	r.sync()

	s := r.status(id)
	if s["head_sha"] != f.Head || s["mergeable"] != true || s["checks"] != "none" {
		t.Fatalf("status: %v", s)
	}
	if s["review_decision"] != "approved" || len(s["reviews"].([]any)) != 1 {
		t.Fatalf("reviews on pages 2 and 3 were not read: %v", s)
	}
	st := r.stored(id)
	if st.Diff == nil || len(st.Diff.Files) != 5 || st.Diff.Additions != 5 || st.Diff.Truncated {
		t.Fatalf("diff: %+v", st.Diff)
	}
	if len(st.Diff.Authors) != 1 {
		t.Fatalf("authors: %v", st.Diff.Authors)
	}
}

func TestRecordedForgejoCommitsLagBehindHead(t *testing.T) {
	f := loadForgejoFixture(t, "forgejo_stale.json")
	if f.Stale == nil || f.Fresh == nil {
		t.Fatal("fixture misses the stale or fresh snapshot")
	}
	var fresh atomic.Bool
	srv := replayForgejo(t, f, "replay-token", func() snapshot {
		if fresh.Load() {
			return *f.Fresh
		}
		return *f.Stale
	})
	r := newForgeRig(t, srv.URL, "replay-token", f.Repo)
	id := r.submit(f.Number)

	r.sync()
	if code, out := r.statusCode(id); code != http.StatusNotFound {
		t.Fatalf("status stored while the commits were behind the head: %d %v", code, out)
	}

	fresh.Store(true)
	r.sync()
	s := r.status(id)
	if s["head_sha"] != f.NewHead {
		t.Fatalf("head = %v, want %s", s["head_sha"], f.NewHead)
	}
	st := r.stored(id)
	if st.Diff == nil || len(st.Diff.Files) != 6 || st.Diff.HeadSHA != f.NewHead {
		t.Fatalf("diff after the commits caught up: %+v", st.Diff)
	}
}

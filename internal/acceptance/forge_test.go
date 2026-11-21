package acceptance

import (
	"crypto/sha1" //nolint:gosec // commit ids of a fake forge
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
)

const repoName = "acme/shop"

type forgePR struct {
	Number int
	Title  string
	Branch string
	SHA    string
	Merged bool
	Files  []string
}

type forgeStatus struct {
	SHA     string
	State   string
	Context string
}

type forge struct {
	srv      *httptest.Server
	bus      *bus
	mu       sync.Mutex
	prs      map[int]*forgePR
	statuses []forgeStatus
	commits  int
}

func newForge(b *bus) *forge {
	f := &forge{bus: b, prs: map[int]*forgePR{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

func (f *forge) close() { f.srv.Close() }

func (f *forge) url() string { return f.srv.URL }

func (f *forge) prURL(n int) string { return fmt.Sprintf("%s/%s/pulls/%d", f.srv.URL, repoName, n) }

func (f *forge) prNumber(u string) int {
	n, _ := strconv.Atoi(u[strings.LastIndex(u, "/")+1:])
	return n
}

func (f *forge) newSHA(seed string) string {
	f.commits++
	sum := sha1.Sum(fmt.Appendf(nil, "%s#%d", seed, f.commits)) //nolint:gosec // commit ids of a fake forge
	return hex.EncodeToString(sum[:])
}

func (f *forge) open(title, branch string, files []string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.prs) + 1
	f.prs[n] = &forgePR{Number: n, Title: title, Branch: branch, SHA: f.newSHA(branch), Files: files}
	return n
}

func (f *forge) repoint(n int, branch string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.prs[n]
	p.Branch, p.SHA = branch, f.newSHA(branch)
}

func (f *forge) merge(n int) {
	f.mu.Lock()
	f.prs[n].Merged = true
	f.mu.Unlock()
	f.bus.fire()
}

func (f *forge) pr(n int) forgePR {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.prs[n]
}

func (f *forge) posted() []forgeStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]forgeStatus(nil), f.statuses...)
}

func (f *forge) state(sha string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.latest(sha)
}

func (f *forge) latest(sha string) string {
	state := ""
	for _, s := range f.statuses {
		if s.SHA == sha {
			state = s.State
		}
	}
	return state
}

func (f *forge) serve(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/repos/"+repoName+"/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(rest, "/")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && parts[0] == "pulls" && len(parts) >= 2:
		f.pull(w, r, parts[1:])
	case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "commits" && parts[2] == "status":
		state := f.latest(parts[1])
		count := 0
		if state != "" {
			count = 1
		}
		writeJSON(w, map[string]any{"state": state, "total_count": count})
	case r.Method == http.MethodPost && len(parts) == 2 && parts[0] == "statuses":
		var st struct {
			State   string `json:"state"`
			Context string `json:"context"`
		}
		if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.statuses = append(f.statuses, forgeStatus{SHA: parts[1], State: st.State, Context: st.Context})
		w.WriteHeader(http.StatusCreated)
		defer f.bus.fire()
	default:
		http.NotFound(w, r)
	}
}

func (f *forge) pull(w http.ResponseWriter, r *http.Request, parts []string) {
	name, diff := strings.CutSuffix(parts[0], ".diff")
	n, err := strconv.Atoi(name)
	p := f.prs[n]
	if err != nil || p == nil {
		http.NotFound(w, r)
		return
	}
	if diff {
		var sb strings.Builder
		for _, file := range p.Files {
			fmt.Fprintf(&sb, "diff --git a/%[1]s b/%[1]s\nnew file mode 100644\n--- /dev/null\n+++ b/%[1]s\n@@ -0,0 +1 @@\n+%[2]s\n", file, p.Title)
		}
		_, _ = w.Write([]byte(sb.String()))
		return
	}
	first := r.URL.Query().Get("page") == "" || r.URL.Query().Get("page") == "1"
	switch {
	case len(parts) == 1:
		writeJSON(w, map[string]any{
			"number": p.Number, "title": p.Title, "state": "open", "merged": p.Merged,
			"html_url": f.prURL(p.Number), "mergeable": true,
			"head": map[string]string{"ref": p.Branch, "sha": p.SHA},
			"base": map[string]string{"ref": "main"},
		})
	case parts[1] == "reviews":
		writeJSON(w, []any{})
	case parts[1] == "files":
		files := []map[string]any{}
		for _, file := range p.Files {
			if first {
				files = append(files, map[string]any{"filename": file, "additions": 1, "deletions": 0})
			}
		}
		writeJSON(w, files)
	case parts[1] == "commits":
		commits := []map[string]any{}
		if first {
			commits = append(commits, map[string]any{"sha": p.SHA, "author": map[string]string{"login": "scripted-agent"}})
		}
		writeJSON(w, commits)
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

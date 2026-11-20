package prompts_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/SigmaUno/subspace/internal/prompts"
)

func get(t *testing.T, h http.Handler, path string, out any) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s: %v: %s", path, err, rec.Body)
		}
	}
	return rec.Code
}

func TestLibraryThroughHandler(t *testing.T) {
	dir := t.TempDir()
	lib := prompts.New(dir)
	h := lib.Handler()

	var list struct{ Prompts []prompts.Template }
	if code := get(t, h, "/prompts", &list); code != http.StatusOK {
		t.Fatalf("list: %d", code)
	}
	ids := map[string]bool{}
	for _, p := range list.Prompts {
		ids[p.ID] = true
	}
	for _, id := range []string{
		"make-10x-faster", "measure-redesign-io-mem-cpu", "differential-test-close-gaps",
		"implicit-state-machines-to-types", "shared-state-to-lock-free", "parallelize-sequential-async",
		"add-otel-measure-bottlenecks", "reimplement-tigerstyle", "believe-in-yourself-8h", "subspace-task-basics",
	} {
		if !ids[id] {
			t.Errorf("missing embedded prompt %s", id)
		}
	}

	var tagged struct{ Prompts []prompts.Template }
	get(t, h, "/prompts?tag=testing", &tagged)
	if len(tagged.Prompts) != 1 || tagged.Prompts[0].ID != "differential-test-close-gaps" {
		t.Fatalf("tag filter: %+v", tagged.Prompts)
	}

	var errBody struct{ Error struct{ Code string } }
	if code := get(t, h, "/prompts/nope", &errBody); code != http.StatusNotFound || errBody.Error.Code != "not_found" {
		t.Fatalf("unknown id: %d %+v", code, errBody)
	}

	var builtin prompts.Template
	get(t, h, "/prompts/make-10x-faster", &builtin)
	if builtin.Title != "Make it 10x faster" {
		t.Fatalf("builtin: %+v", builtin)
	}

	override := "---\nid: make-10x-faster\ntitle: Owner version\ntags: [performance]\nvariables: [target]\n---\nOwner says: speed up {{target}}.\n"
	if err := os.WriteFile(filepath.Join(dir, "mine.md"), []byte(override), 0o600); err != nil {
		t.Fatal(err)
	}
	var got prompts.Template
	get(t, h, "/prompts/make-10x-faster", &got)
	if got.Title != "Owner version" {
		t.Fatalf("override not applied: %+v", got)
	}

	out, err := lib.Render("make-10x-faster", map[string]string{"target": "the sweeper"})
	if err != nil || out != "Owner says: speed up the sweeper." {
		t.Fatalf("render: %q %v", out, err)
	}
}

func TestRenderRejectsMissingVariables(t *testing.T) {
	lib := prompts.New("")
	if _, err := lib.Render("subspace-task-basics", map[string]string{"agent_id": "a"}); err == nil {
		t.Fatal("expected missing variable error")
	}
	out, err := lib.Render("subspace-task-basics", map[string]string{"agent_id": "claude-1", "task_id": "T-3"})
	if err != nil || out == "" {
		t.Fatalf("render: %v", err)
	}
}

func TestBrokenOverrideSurfacesAsError(t *testing.T) {
	dir := t.TempDir()
	bad := "---\nid: x\ntitle: X\nvariables: []\n---\nuses {{undeclared}}\n"
	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	var errBody struct{ Error struct{ Code string } }
	if code := get(t, prompts.New(dir).Handler(), "/prompts", &errBody); code != http.StatusInternalServerError {
		t.Fatalf("code %d", code)
	}
}

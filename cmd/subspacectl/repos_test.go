package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReposRegistryCommands(t *testing.T) {
	h := newHarness(t)
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/o/r":
			_, _ = w.Write([]byte(`{"default_branch":"main","permissions":{"push":true}}`))
		case "/api/v1/repos/o/r/branches/main":
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer forge.Close()

	added := h.object("repos", "add", "o/r", "--host", "forgejo", "--base-url", forge.URL, "--protect", "main", "--protect", "release/*",
		"--merge-test", "make test", "--merge-workdir", "/data/mq", "--token-env", "FORGE_TOKEN")
	if added["name"] != "o/r" || added["enabled"] != true || added["default_branch"] != "main" || added["token_env"] != "FORGE_TOKEN" ||
		len(added["protected_branches"].([]any)) != 2 || len(added["merge_test_command"].([]any)) != 2 {
		t.Fatalf("add = %v", added)
	}
	h.fails(exitConflict, "repos", "add", "o/r", "--host", "github")
	h.fails(exitUsage, "repos", "add", "o/r")
	h.fails(exitUsage, "repos", "add", "no-owner", "--host", "github")
	h.fails(exitUsage, "repos", "add", "o/secret", "--host", "github", "--token-env", "ghp_16C7e42F292c6912E7710c838347Ae178B4a")

	updated := h.object("repos", "update", "o/r", "--default-branch", "trunk", "--enabled=false")
	if updated["default_branch"] != "trunk" || updated["enabled"] != false || updated["base_url"] != forge.URL ||
		len(updated["protected_branches"].([]any)) != 2 || updated["merge_work_dir"] != "/data/mq" {
		t.Fatalf("update must keep the fields it was not given: %v", updated)
	}
	cleared := h.object("repos", "update", "o/r", "--merge-test", "", "--enabled=true")
	if len(cleared["merge_test_command"].([]any)) != 0 || cleared["enabled"] != true {
		t.Fatalf("clearing: %v", cleared)
	}
	h.fails(exitUsage, "repos", "update", "o/r")
	h.fails(exitNotFound, "repos", "update", "o/ghost", "--host", "github")

	if shown := h.object("repos", "show", "o/r"); shown["default_branch"] != "trunk" {
		t.Fatalf("show = %v", shown)
	}
	if got := list(t, h.object("repos", "list"), "repos"); len(got) != 1 {
		t.Fatalf("list = %v", got)
	}
	h.object("repos", "update", "o/r", "--default-branch", "main")
	if health := h.object("repos", "health", "o/r"); health["ok"] != true || health["reachable"] != true {
		t.Fatalf("health = %v", health)
	}

	h.text("repos", "remove", "o/r")
	h.fails(exitNotFound, "repos", "show", "o/r")
	h.fails(exitNotFound, "repos", "remove", "o/r")
}

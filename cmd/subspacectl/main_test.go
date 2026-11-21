package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestRequests(t *testing.T) {
	tests := []struct {
		name, method, path, query string
		args                      []string
		input, body               string
	}{
		{"briefs", "GET", "/api/v1/briefs", "limit=1000&status=open", []string{"briefs", "--status=open"}, "", ""},
		{"create", "POST", "/api/v1/briefs", "", []string{"create-brief", "--title", "Work", "--body-file", "-", "--repos", "a/b, c/d", "--max-parallel-agents", "300"},
			"Build it.\n", `{"title":"Work","body":"Build it.\n","repos":["a/b","c/d"],"allowed_providers":[],"max_cost_eur":50,"max_parallel_agents":300}`},
		{"board", "GET", "/api/v1/briefs/B-1/board", "", []string{"board", "B-1"}, "", ""},
		{"approve", "POST", "/api/v1/plans/P-1/approve", "", []string{"approve-plan", "P-1", "--note", "looks good"}, "", `{"note":"looks good"}`},
		{"reject", "POST", "/api/v1/plans/P-2/reject", "", []string{"reject-plan", "--reason=over budget", "P-2"}, "", `{"reason":"over budget"}`},
		{"answer", "POST", "/api/v1/questions/Q-1/answer", "", []string{"answer", "Q-1", "--answer", "yes"}, "", `{"answer":"yes"}`},
		{"pause", "POST", "/api/v1/pause-all", "", []string{"pause-all", "--reason", "maintenance"}, "", `{"reason":"maintenance"}`},
		{"resume", "POST", "/api/v1/resume-all", "", []string{"resume-all"}, "", `{}`},
		{"agent", "POST", "/api/v1/agents", "", []string{"register-agent", "worker-1", "--role", "worker", "--kind", "codex", "--model", "gpt"},
			"", `{"id":"worker-1","role":"worker","kind":"codex","model":"gpt","provider":""}`},
		{"bulk", "POST", "/api/v1/agents/bulk", "", []string{"register-agents", "--prefix", "worker", "--count", "300", "--role", "worker", "--kind", "opencode", "--model", "deepseek-chat"},
			"", `{"prefix":"worker","count":300,"role":"worker","kind":"opencode","model":"deepseek-chat","provider":""}`},
		{"revoke", "POST", "/api/v1/agents/worker-1/revoke", "", []string{"revoke-agent", "worker-1"}, "", `{"reason":""}`},
		{"list agents", "GET", "/api/v1/agents", "limit=5&role=tester&status=active", []string{"agents", "--role", "tester", "--status", "active", "--limit", "5"}, "", ""},
		{"get capacity", "GET", "/api/v1/capacity", "", []string{"capacity", "get"}, "", ""},
		{"set capacity", "PUT", "/api/v1/capacity", "", []string{"capacity", "set", "--file", "-"},
			`{"global_max_agents":300,"limits":[]}`, `{"global_max_agents":300,"limits":[]}`},
		{"connect", "GET", "/api/v1/agents/worker-1/connect", "", []string{"join-snippet", "worker-1"}, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			const token = "owner-token-for-tests-only"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tt.method || r.URL.Path != tt.path || r.URL.RawQuery != tt.query {
					t.Errorf("request = %s %s, want %s %s?%s", r.Method, r.URL, tt.method, tt.path, tt.query)
				}
				if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("Accept") != "application/json" {
					t.Error("expected JSON accept and bearer auth")
				}
				got, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if tt.body == "" {
					if len(got) != 0 {
						t.Errorf("unexpected body %s", got)
					}
				} else {
					var gotJSON, wantJSON any
					if err := json.Unmarshal(got, &gotJSON); err != nil {
						t.Error(err)
					}
					if err := json.Unmarshal([]byte(tt.body), &wantJSON); err != nil {
						t.Error(err)
					}
					if !reflect.DeepEqual(gotJSON, wantJSON) || r.Header.Get("Content-Type") != "application/json" {
						t.Errorf("expected JSON body %s, got %s", tt.body, got)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				if tt.name == "agent" {
					w.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(w, `{"connect":{"token":"ssp_once"}}`)
					return
				}
				_, _ = io.WriteString(w, `{"ok":true}`)
			}))
			defer server.Close()
			env := func(key string) string {
				return map[string]string{"SUBSPACE_URL": server.URL, "SUBSPACE_OWNER_TOKEN": token}[key]
			}
			if err := run(t.Context(), tt.args, env, strings.NewReader(tt.input), &output); err != nil {
				t.Fatal(err)
			}
			if !json.Valid(output.Bytes()) {
				t.Fatalf("invalid CLI JSON: %s", &output)
			}
			if tt.name == "agent" && strings.Count(output.String(), "ssp_once") != 1 {
				t.Fatalf("expected the server's one-time token in output")
			}
		})
	}
}

func TestInvalidArguments(t *testing.T) {
	tests := [][]string{
		{"unknown"},
		{"board", "../state"},
		{"board", "B-1", "B-2"},
		{"create-brief", "--title", "title", "--body", "body", "--body-file", "-"},
		{"answer", "Q-1"},
		{"answer", "Q-1", "--answer"},
		{"reject-plan", "P-1", "--reason", " "},
		{"resume-all", "unexpected"},
		{"register-agent", "a", "--id=b", "--role=worker", "--kind=codex", "--model=gpt"},
		{"register-agents", "--prefix=a", "--count=1001", "--role=worker", "--kind=codex", "--model=gpt"},
		{"agents", "--limit=1001"},
		{"capacity", "set", "--file=-"},
	}
	for _, args := range tests {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			err := run(t.Context(), args, func(string) string {
				t.Error("invalid arguments should fail before reading credentials")
				return ""
			}, strings.NewReader("[]"), io.Discard)
			if err == nil {
				t.Fatal("expected an argument error")
			}
		})
	}
}

func TestHTTPFailureDoesNotPrintResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":{"code":"conflict","message":"plan already decided"}}`)
	}))
	defer server.Close()

	var output bytes.Buffer
	err := run(t.Context(), []string{"approve-plan", "P-1"}, mapEnv(map[string]string{"SUBSPACE_URL": server.URL, "SUBSPACE_OWNER_TOKEN": "token"}), strings.NewReader(""), &output)
	if err == nil || err.Error() != "HTTP 409 (conflict): plan already decided" || output.Len() != 0 || exitCode(err) != exitConflict {
		t.Fatalf("error = %v; output = %q", err, &output)
	}
}

func TestHTTPRedirectDoesNotForwardOwnerToken(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("redirect was followed")
	}))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	err := run(t.Context(), []string{"agents"}, mapEnv(map[string]string{"SUBSPACE_URL": server.URL, "SUBSPACE_OWNER_TOKEN": "token"}), strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("expected a redirect error, got %v", err)
	}
}

func mapEnv(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

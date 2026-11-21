package mergequeue

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const authToken = "ghp_Zx81QvLmTestTokenValue0042"

type gitHTTP struct {
	url      string
	requests atomic.Int32
}

func (e *env) serveGit(want string) *gitHTTP {
	e.t.Helper()
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		e.t.Skip("git exec path unavailable")
	}
	backend := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	e.git(e.remote, "config", "http.receivepack", "true")
	g := &gitHTTP{}
	h := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + e.root, "GIT_HTTP_EXPORT_ALL=1"},
		InheritEnv: []string{"PATH", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.requests.Add(1)
		if r.Header.Get("Authorization") != want {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	e.t.Cleanup(srv.Close)
	g.url = srv.URL + "/remote.git"
	return g
}

func (e *env) remoteQueue(rc RepoConfig) *Queue {
	e.t.Helper()
	rc.Verify, rc.Auto = []string{"verify"}, true
	q, err := New(Config{
		Repos:    map[string]RepoConfig{repo: rc},
		WorkDir:  filepath.Join(e.root, "work"),
		Store:    e.store,
		Runner:   &trainRunner{},
		Notifier: e.rec,
		Gate:     e.rec,
		Tasks:    e.rec,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return q
}

func (e *env) logs() string {
	e.t.Helper()
	out := strings.Join(e.rec.notes, "\n")
	for _, c := range e.states() {
		out += "\n" + c.Log
	}
	trains, err := e.store.ListTrains(context.Background(), repo, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, tr := range trains {
		out += "\n" + tr.Log
	}
	return out
}

func TestMergePushesWithHostCredentials(t *testing.T) {
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+authToken))
	cases := []struct{ name, host, header string }{
		{"github", "github", basic},
		{"forgejo", HostForgejo, "token " + authToken},
	}
	for _, tc := range cases {
		for _, train := range []int{0, 2} {
			t.Run(fmt.Sprintf("%s/train=%d", tc.name, train), func(t *testing.T) {
				t.Setenv("GIT_TRACE_CURL", "1")
				t.Setenv("GIT_TRACE_REDACT", "0")
				t.Setenv("MQ_TEST_GIT_TOKEN", authToken)
				e := newEnv(t)
				e.branch("feature", "feature.txt", "x\n")
				srv := e.serveGit(tc.header)
				q := e.remoteQueue(RepoConfig{Remote: srv.url, Host: tc.host, TokenEnv: "MQ_TEST_GIT_TOKEN", Token: authToken, MaxTrain: train})
				e.enqueue(q, 7, "feature", "T-1")

				if train > 0 {
					if tr := e.train(q); tr.State != TrainMerged || e.ref("main") != tr.Tip {
						t.Fatalf("train %s\n%s", tr.State, tr.Log)
					}
				} else if c := e.process(q); c.State != StateMerged || e.ref("main") != c.MergedSHA {
					t.Fatalf("candidate %s\n%s", c.State, c.Log)
				}
				if !e.onMain("feature.txt") {
					t.Fatal("feature.txt was not pushed to main")
				}
				logs := e.logs()
				if !strings.Contains(logs, "[redacted]") {
					t.Fatalf("git trace did not exercise redaction:\n%s", logs)
				}
				for _, secret := range []string{authToken, strings.TrimPrefix(basic, "Basic ")} {
					if strings.Contains(logs, secret) {
						t.Fatalf("credential leaked into logs or thread notes:\n%s", logs)
					}
				}
			})
		}
	}
}

func TestMissingTokenEnvEjectsNamingVariable(t *testing.T) {
	for _, train := range []int{0, 2} {
		e := newEnv(t)
		e.branch("feature", "feature.txt", "x\n")
		srv := e.serveGit("token " + authToken)
		q := e.remoteQueue(RepoConfig{Remote: srv.url, Host: HostForgejo, TokenEnv: "MQ_TEST_UNSET_TOKEN", MaxTrain: train})
		e.enqueue(q, 7, "feature", "T-1")

		if train > 0 {
			e.idle(q)
		} else {
			e.process(q)
		}
		if st := e.states()["T-1"]; st.State != StateEjected {
			t.Fatalf("train %d: state %s", train, st.State)
		}
		if !e.note("T-1", "MQ_TEST_UNSET_TOKEN") || len(e.rec.changes) != 1 || !strings.Contains(e.rec.changes[0], "MQ_TEST_UNSET_TOKEN") {
			t.Fatalf("train %d: notes %q changes %q", train, e.rec.notes, e.rec.changes)
		}
		if srv.requests.Load() != 0 || e.onMain("feature.txt") {
			t.Fatalf("train %d: merge queue contacted the host without a token", train)
		}
	}
}

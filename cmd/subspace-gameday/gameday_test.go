package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func shortOptions() Options {
	return Options{
		Detect: 8 * time.Second, Recover: 15 * time.Second, Lease: time.Second,
		Agents: 3, Subs: 40, Burst: 100, Storm: 300 * time.Millisecond, Seed: 7,
		PGDSN: os.Getenv("SUBSPACE_TEST_PG_DSN"), Log: quietLog(),
	}
}

func TestScenariosInProcess(t *testing.T) {
	opt := shortOptions()
	for _, s := range scenarios {
		t.Run(s.Name, func(t *testing.T) {
			res := execute(context.Background(), opt, s)
			if res.Status == Skip {
				t.Skip(res.Note)
			}
			for _, c := range res.Checks {
				if !c.OK {
					t.Errorf("%s: %s", c.Name, c.Detail)
				}
			}
			if res.Error != "" {
				t.Fatal(res.Error)
			}
			if res.DetectMS == nil && s.Name != "sse-storm" {
				t.Error("no detection time recorded")
			}
			if res.RecoverMS == nil {
				t.Error("no recovery time recorded")
			}
		})
	}
}

func TestRemoteTarget(t *testing.T) {
	opt := shortOptions()
	st, err := opt.NewStack()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	t.Setenv("SUBSPACE_OWNER_TOKEN", st.Token)

	var out, errb bytes.Buffer
	args := []string{"-url", st.URL, "-json", "-subscribers", "20", "-burst", "60", "-detect", "8s", "-recover", "15s"}
	if code := run(context.Background(), args, &out, &errb); code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errb.String())
	}
	var rep struct {
		Passed    bool     `json:"passed"`
		Scenarios []Result `json:"scenarios"`
	}
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	status := map[string]Status{}
	for _, r := range rep.Scenarios {
		status[r.Name] = r.Status
	}
	want := map[string]Status{
		"runaway-agent": Pass, "leaked-token": Pass, "sse-storm": Pass,
		"budget-exhaustion": Skip, "server-restart": Skip, "webhook-down": Skip, "leader-loss": Skip,
	}
	for name, s := range want {
		if status[name] != s {
			t.Errorf("%s: %s, want %s", name, status[name], s)
		}
	}
	if !rep.Passed {
		t.Error("report not passed")
	}
}

func TestRemoteDisruptive(t *testing.T) {
	opt := shortOptions()
	st, err := opt.NewStack()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	t.Setenv("SUBSPACE_OWNER_TOKEN", st.Token)

	var out, errb bytes.Buffer
	args := []string{"-url", st.URL, "-scenario", "budget-exhaustion", "-disruptive", "-json"}
	if code := run(context.Background(), args, &out, &errb); code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errb.String())
	}
	state, err := st.Client().Must(context.Background(), "GET", "/state", nil, 200)
	if err != nil {
		t.Fatal(err)
	}
	if state["paused"] != false {
		t.Fatalf("target left paused: %v", state)
	}
}

func TestFailingExpectationExitsNonZero(t *testing.T) {
	var out, errb bytes.Buffer
	args := []string{"-scenario", "runaway-agent", "-detect", "1ns"}
	if code := run(context.Background(), args, &out, &errb); code != 1 {
		t.Fatalf("exit %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "FAIL") || !strings.Contains(out.String(), "detected within limit") {
		t.Fatalf("table lacks the failed expectation:\n%s", out.String())
	}
}

func TestUnknownScenario(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"-scenario", "nope"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestEnvironmentNeverSelectsARemoteTarget(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	t.Setenv("SUBSPACE_URL", srv.URL)
	t.Setenv("SUBSPACE_OWNER_TOKEN", strings.Repeat("o", 32))
	var out, errb bytes.Buffer
	run(context.Background(), []string{"-scenario", "runaway-agent", "-detect", "1ns"}, &out, &errb)
	if n := hits.Load(); n != 0 {
		t.Fatalf("gameday sent %d requests to the server named by SUBSPACE_URL", n)
	}
}

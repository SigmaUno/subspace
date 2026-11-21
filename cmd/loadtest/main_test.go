package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/loadtest"
)

const owner = "fake-owner-token-0123456789abcdef"

func TestRunOK(t *testing.T) {
	ts := httptest.NewServer(loadtest.NewFakeServer(owner))
	defer ts.Close()
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"-url", ts.URL, "-owner-token", owner, "-agents", "8", "-duration", "300ms",
		"-sse-subs", "3", "-think", "1ms", "-json"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d, stderr %s, stdout %s", code, errb.String(), out.String())
	}
	var rep loadtest.Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("json report: %v\n%s", err, out.String())
	}
	if rep.Agents != 8 || rep.Requests == 0 || rep.SSE.Subscribers != 3 {
		t.Fatalf("report %+v", rep)
	}
}

func TestRunBreachExitCode(t *testing.T) {
	f := loadtest.NewFakeServer(owner)
	f.FailTool, f.FailEvery = "claim_task", 2
	ts := httptest.NewServer(f)
	defer ts.Close()
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"-url", ts.URL, "-owner-token", owner, "-agents", "4", "-duration", "300ms",
		"-sse-subs", "0", "-think", "1ms"}, &out, &errb)
	if code != 1 || !strings.Contains(out.String(), "RESULT: FAIL") || !strings.Contains(errb.String(), "exceeds -max-error-rate") {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out.String(), errb.String())
	}
}

func TestRunUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"-agents", "0"},
		{"-url", "http://127.0.0.1:1", "-owner-token", ""},
		{"-nope"},
		{"extra"},
		{"-scenario", "nope"},
		{"-scenario", "restart", "-url", "http://127.0.0.1:1", "-owner-token", "x"},
		{"-scenario", "big-plan", "-tasks", "501"},
	} {
		var out, errb bytes.Buffer
		if code := run(context.Background(), args, &out, &errb); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

func TestRunScenario(t *testing.T) {
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"-scenario", "thread-heavy", "-agents", "3", "-messages", "20", "-duration", "10s",
		"-think", "1ms", "-sse-subs", "0", "-json"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d, stderr %s, stdout %s", code, errb.String(), out.String())
	}
	var rep loadtest.Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("json report: %v\n%s", err, out.String())
	}
	if rep.Scenario != loadtest.ScenarioThreadHeavy || len(rep.Invariants) == 0 || rep.InvariantsFailed() != 0 {
		t.Fatalf("report %+v", rep)
	}
}

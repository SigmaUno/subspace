package incident_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/incident"
	"github.com/SigmaUno/subspace/internal/store"
)

func serve(t *testing.T, svc *incident.Service) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(incident.Handler(svc, slog.New(slog.DiscardHandler)))
	t.Cleanup(ts.Close)
	return ts
}

func call(t *testing.T, ts *httptest.Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("%s %s: content type %q", method, path, ct)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp.StatusCode, out
}

func errCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func insertIncidents(t *testing.T, st store.Store, n int) {
	t.Helper()
	err := st.Atomic(context.Background(), func(tx store.Tx) error {
		q := tx.(interface {
			Exec(string, ...any) (sql.Result, error)
		})
		for i := 1; i <= n; i++ {
			if _, err := q.Exec(`INSERT INTO incidents (id, reason, actions, results, started_at, ended_at, actor) VALUES (?, ?, '[]', '[]', ?, ?, 'owner')`,
				fmt.Sprintf("INC-%04d", i), "old", int64(i)*1000, int64(i)*1000+1); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestHTTPStartListEnd(t *testing.T) {
	ts := serve(t, newService(t, openStore(t), &fakeControl{}, nil, ""))
	body := `{"reason":"leak","actions":["notify"]}`

	code, out := call(t, ts, "POST", "/incident", body)
	if code != http.StatusCreated || out["id"] != "INC-1" || out["succeeded"] != 1.0 {
		t.Fatalf("start: %d %v", code, out)
	}
	code, out = call(t, ts, "POST", "/incident", body)
	if code != http.StatusOK || out["id"] != "INC-1" {
		t.Fatalf("restart: %d %v", code, out)
	}
	code, out = call(t, ts, "POST", "/incident/INC-1/end", "")
	if code != http.StatusOK || out["ended_at"] == nil {
		t.Fatalf("end: %d %v", code, out)
	}
	code, out = call(t, ts, "POST", "/incident/INC-1/end", `{"resume_agents":true,"note":"again"}`)
	if code != http.StatusOK || out["ended_at"] == nil {
		t.Fatalf("end again: %d %v", code, out)
	}
	code, out = call(t, ts, "GET", "/incident", "")
	if list, _ := out["incidents"].([]any); code != http.StatusOK || len(list) != 1 {
		t.Fatalf("list: %d %v", code, out)
	}
}

func TestHTTPRejectsInvalidRequests(t *testing.T) {
	ts := serve(t, newService(t, openStore(t), &fakeControl{}, nil, ""))
	if code, out := call(t, ts, "POST", "/incident", `{"reason":"x","actions":["notify"]}`); code != http.StatusCreated {
		t.Fatalf("seed: %d %v", code, out)
	}
	for _, tc := range []struct {
		method, path, body string
		status             int
		code               string
	}{
		{"POST", "/incident", "", http.StatusBadRequest, "bad_request"},
		{"POST", "/incident", "{", http.StatusBadRequest, "bad_request"},
		{"POST", "/incident", `{"reason":"x","actions":["notify"],"force":true}`, http.StatusBadRequest, "bad_request"},
		{"POST", "/incident", `{"reason":"","actions":["notify"]}`, http.StatusBadRequest, "bad_request"},
		{"POST", "/incident/INC-1/end", "{", http.StatusBadRequest, "bad_request"},
		{"POST", "/incident/INC-1/end", `{"note":"x"`, http.StatusBadRequest, "bad_request"},
		{"POST", "/incident/INC-1/end", `{"force":true}`, http.StatusBadRequest, "bad_request"},
		{"POST", "/incident/INC-9/end", "", http.StatusNotFound, "not_found"},
		{"GET", "/incident?limit=abc", "", http.StatusBadRequest, "bad_request"},
		{"GET", "/incident?limit=0", "", http.StatusBadRequest, "bad_request"},
		{"GET", "/incident?limit=1001", "", http.StatusBadRequest, "bad_request"},
	} {
		code, out := call(t, ts, tc.method, tc.path, tc.body)
		if code != tc.status || errCode(out) != tc.code {
			t.Fatalf("%s %s %q: %d %v", tc.method, tc.path, tc.body, code, out)
		}
	}
	code, out := call(t, ts, "GET", "/incident", "")
	if list, _ := out["incidents"].([]any); code != http.StatusOK || len(list) != 1 || list[0].(map[string]any)["ended_at"] != nil {
		t.Fatalf("rejected requests changed state: %d %v", code, out)
	}
}

func TestHTTPListLimits(t *testing.T) {
	st := openStore(t)
	insertIncidents(t, st, 101)
	ts := serve(t, newService(t, st, &fakeControl{}, nil, ""))
	for _, tc := range []struct {
		query string
		n     int
		first string
	}{
		{"", 100, "INC-0101"},
		{"?limit=1", 1, "INC-0101"},
		{"?limit=1000", 101, "INC-0101"},
	} {
		code, out := call(t, ts, "GET", "/incident"+tc.query, "")
		list, _ := out["incidents"].([]any)
		if code != http.StatusOK || len(list) != tc.n || list[0].(map[string]any)["id"] != tc.first {
			t.Fatalf("limit %q: %d, %d incidents", tc.query, code, len(list))
		}
	}
}

func TestHTTPBodyLimit(t *testing.T) {
	ts := serve(t, newService(t, openStore(t), &fakeControl{}, nil, ""))
	body := `{"reason":"x","actions":["notify"]}`
	pad := func(n int) string { return strings.Repeat(" ", n-len(body)) + body }

	if code, out := call(t, ts, "POST", "/incident", pad(64<<10+512)); code != http.StatusBadRequest {
		t.Fatalf("oversized body: %d %v", code, out)
	}
	if code, out := call(t, ts, "POST", "/incident", pad(64<<10-512)); code != http.StatusCreated {
		t.Fatalf("body under limit: %d %v", code, out)
	}
}

func TestHTTPStoreFailureIsInternal(t *testing.T) {
	ts := serve(t, newService(t, plainStore{openStore(t)}, &fakeControl{}, nil, ""))
	code, out := call(t, ts, "GET", "/incident", "")
	if code != http.StatusInternalServerError || errCode(out) != "internal" || out["error"].(map[string]any)["message"] != "internal error" {
		t.Fatalf("list: %d %v", code, out)
	}
}

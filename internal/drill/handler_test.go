package drill_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/drill"
)

func serveDrills(t *testing.T, svc *drill.Service) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(drill.Handler(svc, slog.New(slog.DiscardHandler)))
	t.Cleanup(ts.Close)
	return ts
}

func request(t *testing.T, ts *httptest.Server, method, path, body string) (int, map[string]any) {
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
		t.Fatal(err)
	}
	return resp.StatusCode, out
}

func TestHandlerListLimits(t *testing.T) {
	r := newRig(t, "")
	for i := 1; i <= 101; i++ {
		execSQL(t, r.live, `INSERT INTO restore_drills (id, status, trigger_kind, started_at, finished_at) VALUES (?, 'ok', 'schedule', ?, ?)`,
			fmt.Sprintf("D-%04d", i), int64(i)*1000, int64(i)*1000+1)
	}
	ts := serveDrills(t, r.svc)
	for _, tc := range []struct {
		query  string
		status int
		n      int
	}{
		{"", http.StatusOK, 100},
		{"?limit=1000", http.StatusOK, 101},
		{"?limit=1001", http.StatusBadRequest, 0},
		{"?limit=x", http.StatusBadRequest, 0},
	} {
		code, out := request(t, ts, "GET", "/drills"+tc.query, "")
		list, _ := out["drills"].([]any)
		if code != tc.status || len(list) != tc.n || tc.n > 0 && list[0].(map[string]any)["id"] != "D-0101" {
			t.Fatalf("%q: %d with %d drills", tc.query, code, len(list))
		}
	}
}

func TestHandlerBodyLimit(t *testing.T) {
	r := newRig(t, "")
	ts := serveDrills(t, r.svc)
	pad := func(n int) string { return strings.Repeat(" ", n-2) + "{}" }
	if code, out := request(t, ts, "POST", "/drills", pad(4<<10+256)); code != http.StatusBadRequest {
		t.Fatalf("oversized body: %d %v", code, out)
	}
	if code, out := request(t, ts, "POST", "/drills", pad(4<<10-256)); code != http.StatusCreated || out["id"] != "D-1" {
		t.Fatalf("body under limit: %d %v", code, out)
	}
}

func TestHandlerStoreFailureIsInternal(t *testing.T) {
	r := newRig(t, "")
	ts := serveDrills(t, newDrill(t, drill.Options{Store: plainStore{r.live}, Backups: r.bk, Dir: r.dir, Now: r.clk.Now}))
	for _, method := range []string{"GET", "POST"} {
		code, out := request(t, ts, method, "/drills", "")
		e, _ := out["error"].(map[string]any)
		if code != http.StatusInternalServerError || e["code"] != "internal" || e["message"] != "internal error" {
			t.Fatalf("%s: %d %v", method, code, out)
		}
	}
}

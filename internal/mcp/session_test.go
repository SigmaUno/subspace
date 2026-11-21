package mcp

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionsDoNotAccumulate(t *testing.T) {
	s, _ := confServer()
	now := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	initialize := func(tok string) string {
		w := post(t, s, tok, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
		return w.Header().Get("Mcp-Session-Id")
	}
	size := func() (int, int) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.sessions), len(s.byAgent)
	}

	for range 1000 {
		sid := initialize("tok-worker")
		r := httptest.NewRequest(http.MethodDelete, "/mcp", nil)
		r.Header.Set("Authorization", "Bearer tok-worker")
		r.Header.Set("Mcp-Session-Id", sid)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent {
			t.Fatalf("delete: %d", w.Code)
		}
	}
	if n, a := size(); n != 0 || a != 0 {
		t.Fatalf("after DELETE: %d sessions, %d agents", n, a)
	}

	for _, tok := range []string{"tok-leader", "tok-tester", "tok-reviewer"} {
		for range 20 {
			initialize(tok)
		}
	}
	if n, a := size(); n != 3*maxSessionsPer || a != 3 {
		t.Fatalf("live: %d sessions, %d agents", n, a)
	}
	now = now.Add(sessionTTL + sessionSweep)
	initialize("tok-worker")
	if n, a := size(); n != 1 || a != 1 {
		t.Fatalf("after expiry: %d sessions, %d agents", n, a)
	}
}

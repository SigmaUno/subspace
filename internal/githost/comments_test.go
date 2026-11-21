package githost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCommenterSpeaksBothHostDialects(t *testing.T) {
	type seen struct{ method, path, auth, body string }
	for _, tc := range []struct {
		style Style
		api   string
		auth  string
	}{
		{StyleGitHub, "", "Bearer tok"},
		{StyleForgejo, "/api/v1", "token tok"},
	} {
		var got []seen
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			got = append(got, seen{r.Method, r.URL.Path, r.Header.Get("Authorization"), in["body"]})
			_, _ = w.Write([]byte(`{"id": 55}`))
		}))
		c := NewCommenter(CommenterConfig{Style: tc.style, APIURL: srv.URL + tc.api + "/", Token: "tok"})
		id, err := c.CreateComment(context.Background(), "o/r", 7, "hello")
		if err != nil || id != 55 {
			t.Fatalf("%s create: %d %v", tc.style, id, err)
		}
		if err := c.UpdateComment(context.Background(), "o/r", 55, "bye"); err != nil {
			t.Fatalf("%s update: %v", tc.style, err)
		}
		srv.Close()
		want := []seen{
			{"POST", tc.api + "/repos/o/r/issues/7/comments", tc.auth, "hello"},
			{"PATCH", tc.api + "/repos/o/r/issues/comments/55", tc.auth, "bye"},
		}
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("%s requests: %+v", tc.style, got)
		}
	}
}

func TestCommenterErrors(t *testing.T) {
	var status int
	var header http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range header {
			w.Header()[k] = v
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"message":"secret-token-echo"}`))
	}))
	defer srv.Close()
	c := NewCommenter(CommenterConfig{Style: StyleGitHub, APIURL: srv.URL, Token: "secret-token-echo"})
	call := func(s int, h http.Header) error {
		status, header = s, h
		return c.UpdateComment(context.Background(), "o/r", 1, "x")
	}
	if err := call(404, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("404: %v", err)
	}
	var se *StatusError
	for s, retry := range map[int]bool{500: true, 502: true, 408: true, 401: false, 403: false, 422: false} {
		err := call(s, nil)
		if !errors.As(err, &se) || se.Status != s || se.Retryable() != retry {
			t.Fatalf("%d: %v retryable=%v", s, err, retry)
		}
		if err.Error() != (&StatusError{Status: s}).Error() {
			t.Fatalf("error leaks the response: %v", err)
		}
	}
	if err := call(429, http.Header{"Retry-After": {"30"}}); !errors.As(err, &se) || se.RetryAfter != 30*time.Second || !se.Retryable() {
		t.Fatalf("429: %v", err)
	}
	if err := call(403, http.Header{"X-Ratelimit-Remaining": {"0"}}); !errors.As(err, &se) || !se.Retryable() {
		t.Fatalf("403 with exhausted rate limit: %v", err)
	}
	if _, err := c.CreateComment(context.Background(), "../x", 1, "x"); err == nil {
		t.Fatal("bad repo accepted")
	}
}

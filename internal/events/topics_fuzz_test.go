package events

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func FuzzTopics(f *testing.F) {
	for _, q := range []string{"", "all", "board,brief:B-1", " task:T-1 , ,agents ", "thread:task:T-1,,", ",,", "agent:me,agent:me"} {
		f.Add(q)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
		r.URL.RawQuery = url.Values{"topics": {raw}}.Encode()

		var want []string
		for _, p := range strings.Split(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				want = append(want, p)
			}
		}
		if len(want) == 0 {
			want = []string{TopicAll}
		}

		got := TopicsFromQuery(r)
		if len(got) != len(want) {
			t.Fatalf("TopicsFromQuery(%q) = %q, want %q", raw, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("TopicsFromQuery(%q) = %q, want %q", raw, got, want)
			}
		}

		h := NewHub()
		sub := h.Subscribe(got)
		defer sub.Close()
		if n := h.Subscribers(); n != 1 {
			t.Fatalf("subscribers = %d, want 1", n)
		}
	})
}

package idempotency

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/SigmaUno/subspace/internal/model"
)

func FuzzArgsHash(f *testing.F) {
	for _, s := range []string{
		``, `null`, `{}`, `[]`, `{"a":1,"b":2}`, `{"b":2,"a":1}`, `{ "a" : 1 }`, `{"a":1.0}`, `{"a":1e400}`,
		`{"n":12345678901234567890}`, `{"a":{"c":[1,2,{"x":null}],"b":"é"}}`, `{"a":1} trailing`, `{"a":`, `"str"`, `0`,
		`{"a":"\u0000\ud800"}`, `[[[[[[[[[[1]]]]]]]]]]`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		h := ArgsHash(raw)
		if len(h) != 64 || strings.Trim(h, "0123456789abcdef") != "" {
			t.Fatalf("hash %q is not 64 lowercase hex characters", h)
		}
		if ArgsHash(raw) != h {
			t.Fatal("ArgsHash is not deterministic")
		}
		var v any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if dec.Decode(&v) != nil || v == nil {
			if h != ArgsHash(json.RawMessage(`{}`)) {
				t.Fatalf("undecodable or null arguments must hash like {}: %q", raw)
			}
			return
		}
		canonical, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if ArgsHash(canonical) != h {
			t.Fatalf("canonical form of %q hashes differently", raw)
		}
	})
}

type memStore struct {
	mu sync.Mutex
	m  map[string]*Record
}

func (s *memStore) GetIdempotency(_ context.Context, agentID, key string, now time.Time) (*Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.m[agentID+"\x00"+key]
	if r == nil || !now.Before(r.ExpiresAt) {
		return nil, nil
	}
	return r, nil
}

func (s *memStore) PutIdempotency(_ context.Context, agentID, key string, r *Record, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[agentID+"\x00"+key] = r
	return nil
}

type lookup struct{}

func (lookup) LookupToken(string) (*model.Agent, error) {
	return &model.Agent{ID: "a1", Status: model.AgentActive}, nil
}

func FuzzKeyReplay(f *testing.F) {
	for _, k := range []string{"k", "", " ", "a\x00b", "é🙂", strings.Repeat("k", MaxKey), strings.Repeat("k", MaxKey+1), `"quoted"`, "a\nb"} {
		f.Add(k, `{"x":1}`, `{"x":2}`)
	}
	f.Add("k", `{"a":1,"b":2}`, `{"b":2,"a":1}`)
	f.Add("k", `{"a":1}`, `null`)
	f.Fuzz(func(t *testing.T, key, args1, args2 string) {
		if !utf8.ValidString(key) || !json.Valid([]byte(args1)) || !json.Valid([]byte(args2)) {
			return
		}
		calls := 0
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":1,"jsonrpc":"2.0","result":{"content":[],"isError":false}}` + "\n"))
		})
		h := Wrap(next, Options{Store: &memStore{m: map[string]*Record{}}, Agents: lookup{}, Now: func() time.Time { return time.Unix(1e9, 0) }})
		call := func(args string) (*httptest.ResponseRecorder, string) {
			body, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": 1, "method": "tools/call",
				"params": map[string]any{"name": "claim_task", "arguments": json.RawMessage(args), "_meta": map[string]any{"idempotencyKey": key}},
			})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
			r.Header.Set("Authorization", "Bearer ssp_x")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			return w, w.Body.String()
		}

		if key == "" {
			call(args1)
			call(args1)
			if calls != 2 {
				t.Fatalf("calls without a key = %d, want 2", calls)
			}
			return
		}
		first, firstBody := call(args1)
		if len(key) > MaxKey {
			if calls != 0 || !strings.Contains(firstBody, "bad_request") {
				t.Fatalf("oversized key: calls=%d body=%s", calls, firstBody)
			}
			return
		}
		if calls != 1 || first.Header().Get(Replay) != "" {
			t.Fatalf("first call: calls=%d replay=%q", calls, first.Header().Get(Replay))
		}
		replay, replayBody := call(args1)
		if calls != 1 || replay.Header().Get(Replay) != "true" || replayBody != firstBody {
			t.Fatalf("replay: calls=%d header=%q\n%s\n%s", calls, replay.Header().Get(Replay), firstBody, replayBody)
		}
		_, otherBody := call(args2)
		if ArgsHash(json.RawMessage(args1)) == ArgsHash(json.RawMessage(args2)) {
			if calls != 1 || otherBody != firstBody {
				t.Fatalf("equivalent arguments were not replayed: calls=%d", calls)
			}
			return
		}
		if calls != 1 || !strings.Contains(otherBody, "idempotency_conflict") {
			t.Fatalf("different arguments under one key: calls=%d body=%s", calls, otherBody)
		}
	})
}

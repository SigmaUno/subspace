package dst

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/idempotency"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
)

type MCPOptions struct {
	FaultSeed     int64
	DropPct       int
	DupPct        int
	NoIdempotency bool
}

type MCPSystem struct {
	*BoardSystem
	o       MCPOptions
	faults  *faultTransport
	clients map[string]*client.MCP
	extend  func(*mcp.Registry)
}

type tokenLookup struct{ b *board.Board }

func (l tokenLookup) LookupToken(tok string) (*model.Agent, error) {
	return l.b.LoadByTokenHash(auth.Hash(tok))
}

func NewMCPSystem(o MCPOptions) Factory {
	return func(dir string, clock *Clock, lease time.Duration) (System, error) {
		s, err := newBoardSystem(dir, clock, lease, nil)
		if err != nil {
			return nil, err
		}
		return newMCP(s, o, nil), nil
	}
}

func newMCP(s *BoardSystem, o MCPOptions, extend func(*mcp.Registry)) *MCPSystem {
	ft := &faultTransport{r: rand.New(rand.NewSource(o.FaultSeed)), drop: o.DropPct, dup: o.DupPct} //nolint:gosec // deterministic simulation
	m := &MCPSystem{BoardSystem: s, o: o, faults: ft, clients: map[string]*client.MCP{}, extend: extend}
	m.wire()
	return m
}

func (s *MCPSystem) wire() {
	lookup := tokenLookup{s.b}
	reg := mcp.NewRegistry(s.b)
	if s.extend != nil {
		s.extend(reg)
	}
	srv := mcp.NewServer(lookup, reg)
	srv.Now = s.clock.Now
	var h http.Handler = srv
	if !s.o.NoIdempotency {
		h = idempotency.Wrap(h, idempotency.Options{Store: s.st, Agents: lookup, Now: s.clock.Now, Log: discard})
	}
	s.faults.h = s.checkHints(h)
}

func (s *MCPSystem) checkHints(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)
		s.inspectHint(body, rec.Body.Bytes())
		maps.Copy(w.Header(), rec.Header())
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}

func (s *MCPSystem) inspectHint(req, resp []byte) {
	var in struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if json.Unmarshal(req, &in) != nil || in.Method != "tools/call" {
		return
	}
	var out struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			Structured struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
					Hint    string `json:"hint"`
				} `json:"error"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if json.Unmarshal(resp, &out) != nil || !out.Result.IsError {
		return
	}
	e := out.Result.Structured.Error
	want := mcp.Hint(e.Code, in.Params.Name)
	var text struct {
		Error struct {
			Hint string `json:"hint"`
		} `json:"error"`
	}
	if len(out.Result.Content) > 0 {
		_ = json.Unmarshal([]byte(out.Result.Content[0].Text), &text)
	}
	switch {
	case e.Hint != want:
		s.fault(InvHint, "%s error %s carries hint %q, want %q", in.Params.Name, e.Code, e.Hint, want)
	case text.Error.Hint != want:
		s.fault(InvHint, "%s error %s: hint missing from the text content", in.Params.Name, e.Code)
	case want != "" && e.Message != "" && strings.Contains(e.Hint, e.Message):
		s.fault(InvHint, "%s error %s: hint echoes the message %q", in.Params.Name, e.Code, e.Message)
	}
}

func (s *MCPSystem) client(op Op) *client.MCP {
	s.mu.Lock()
	tok := ""
	if toks := s.tokens[op.Actor]; op.Token >= 0 && op.Token < len(toks) {
		tok = toks[op.Token]
	}
	s.mu.Unlock()
	key := op.Actor + "/" + tok
	if c, ok := s.clients[key]; ok {
		return c
	}
	prefix, n := fmt.Sprintf("%s-%d", op.Actor, len(s.clients)), 0
	c := client.NewMCP("http://dst/mcp", tok,
		client.WithHTTPClient(&http.Client{Transport: s.faults}),
		client.WithSleep(func(context.Context, time.Duration) error { return nil }),
		client.WithIdempotencyKeys(func() string { n++; return fmt.Sprintf("%s-%d", prefix, n) }))
	s.clients[key] = c
	return c
}

func (s *MCPSystem) Do(ctx context.Context, op Op) Result {
	s.touch(op)
	var err error
	switch op.Kind {
	case OpRestart:
		res := s.BoardSystem.Do(ctx, op)
		s.wire()
		return res
	case OpAssign:
		_, err = s.client(op).Assign(ctx, op.Task, op.Target, "assignee")
	case OpAssignReviewer:
		_, err = s.client(op).Assign(ctx, op.Task, op.Target, "reviewer")
	case OpReassign:
		_, err = s.client(op).Reassign(ctx, op.Task, op.Target, "dst")
	case OpClaim:
		var t *model.Task
		if t, err = s.client(op).ClaimTask(ctx, op.Task); err == nil {
			return Result{Lease: t.LeaseExpiresAt}
		}
	case OpHeartbeat:
		var hb *board.HeartbeatResult
		if hb, err = s.client(op).Heartbeat(ctx, op.Task); err == nil {
			return Result{Lease: hb.LeaseExpiresAt, Paused: hb.Paused}
		}
	case OpUpdate:
		pct, cost := op.Pct, op.Cost
		_, err = s.client(op).UpdateStatus(ctx, board.StatusInput{TaskID: op.Task, Status: model.StatusInProgress, ProgressPct: &pct, CostEUR: &cost, Note: "progress"})
	case OpBlock:
		_, err = s.client(op).UpdateStatus(ctx, board.StatusInput{TaskID: op.Task, Status: model.StatusBlocked, Note: "stuck"})
	case OpSubmit:
		_, err = s.client(op).Submit(ctx, board.SubmitInput{TaskID: op.Task, PRURL: prURL(op.Task), Summary: "done"})
	case OpRelease:
		_, err = s.client(op).ReleaseTask(ctx, op.Task, "dst")
	case OpReview:
		verdict, comments := "changes_requested", "fix it"
		if op.Approve {
			verdict, comments = "approve", "lgtm"
		}
		_, err = s.client(op).Review(ctx, op.Task, verdict, comments)
	case OpAskOwner:
		_, err = s.client(op).AskOwner(ctx, op.Task, "which way?", "a", "b")
	default:
		return s.BoardSystem.Do(ctx, op)
	}
	return Result{Err: toolErr(err)}
}

func toolErr(err error) error {
	var ce *client.Error
	if errors.As(err, &ce) && ce.Code != "" {
		return &model.Error{Code: ce.Code, Message: ce.Message}
	}
	return err
}

var errDropped = errors.New("dst: response dropped")

type faultTransport struct {
	h       http.Handler
	r       *rand.Rand
	drop    int
	dup     int
	dropped int
}

func (f *faultTransport) serve(req *http.Request, body []byte) *http.Response {
	r := req.Clone(req.Context())
	r.Body = io.NopCloser(bytes.NewReader(body))
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec.Result()
}

func (f *faultTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = b
	}
	if !keyed(body) {
		return f.serve(req, body), nil
	}
	switch p := f.r.Intn(100); {
	case p < f.drop && f.dropped < 2:
		f.dropped++
		f.serve(req, body)
		return nil, errDropped
	case p < f.drop+f.dup:
		f.serve(req, body)
	}
	f.dropped = 0
	return f.serve(req, body), nil
}

func keyed(body []byte) bool {
	var m struct {
		Params struct {
			Meta struct {
				Key string `json:"idempotencyKey"`
			} `json:"_meta"`
		} `json:"params"`
	}
	return json.Unmarshal(body, &m) == nil && m.Params.Meta.Key != ""
}

package dst

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
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
	faults  *faultTransport
	clients map[string]*client.MCP
}

type tokenLookup struct{ b *board.Board }

func (l tokenLookup) LookupToken(tok string) (*model.Agent, error) {
	return l.b.LoadByTokenHash(auth.Hash(tok))
}

func NewMCPSystem(o MCPOptions) Factory {
	return func(dir string, clock *Clock, lease time.Duration) (System, error) {
		s, err := NewBoardSystem(dir, clock, lease)
		if err != nil {
			return nil, err
		}
		bs := s.(*BoardSystem)
		lookup := tokenLookup{bs.b}
		srv := mcp.NewServer(lookup, mcp.NewRegistry(bs.b))
		srv.Now = clock.Now
		var h http.Handler = srv
		if !o.NoIdempotency {
			h = idempotency.Wrap(h, idempotency.Options{Store: bs.st, Agents: lookup, Now: clock.Now,
				Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
		}
		ft := &faultTransport{h: h, r: rand.New(rand.NewSource(o.FaultSeed)), drop: o.DropPct, dup: o.DupPct} //nolint:gosec // deterministic simulation
		return &MCPSystem{BoardSystem: bs, faults: ft, clients: map[string]*client.MCP{}}, nil
	}
}

func (s *MCPSystem) client(id string) *client.MCP {
	if c, ok := s.clients[id]; ok {
		return c
	}
	n := 0
	c := client.NewMCP("http://dst/mcp", s.tokens[id],
		client.WithHTTPClient(&http.Client{Transport: s.faults}),
		client.WithSleep(func(context.Context, time.Duration) error { return nil }),
		client.WithIdempotencyKeys(func() string { n++; return fmt.Sprintf("%s-%d", id, n) }))
	s.clients[id] = c
	return c
}

func (s *MCPSystem) Do(ctx context.Context, op Op) Result {
	var err error
	switch op.Kind {
	case OpAssign:
		_, err = s.client(op.Actor).Assign(ctx, op.Task, op.Target, "assignee")
	case OpAssignReviewer:
		_, err = s.client(op.Actor).Assign(ctx, op.Task, op.Target, "reviewer")
	case OpReassign:
		_, err = s.client(op.Actor).Reassign(ctx, op.Task, op.Target, "dst")
	case OpClaim:
		var t *model.Task
		if t, err = s.client(op.Actor).ClaimTask(ctx, op.Task); err == nil {
			return Result{Lease: t.LeaseExpiresAt}
		}
	case OpHeartbeat:
		var hb *board.HeartbeatResult
		if hb, err = s.client(op.Actor).Heartbeat(ctx, op.Task); err == nil {
			return Result{Lease: hb.LeaseExpiresAt, Paused: hb.Paused}
		}
	case OpUpdate:
		pct, cost := op.Pct, op.Cost
		_, err = s.client(op.Actor).UpdateStatus(ctx, board.StatusInput{TaskID: op.Task, Status: model.StatusInProgress, ProgressPct: &pct, CostEUR: &cost, Note: "progress"})
	case OpBlock:
		_, err = s.client(op.Actor).UpdateStatus(ctx, board.StatusInput{TaskID: op.Task, Status: model.StatusBlocked, Note: "stuck"})
	case OpSubmit:
		_, err = s.client(op.Actor).Submit(ctx, board.SubmitInput{TaskID: op.Task, PRURL: "https://git.example/pr/" + op.Task, Summary: "done"})
	case OpRelease:
		_, err = s.client(op.Actor).ReleaseTask(ctx, op.Task, "dst")
	case OpReview:
		verdict, comments := "changes_requested", "fix it"
		if op.Approve {
			verdict, comments = "approve", "lgtm"
		}
		_, err = s.client(op.Actor).Review(ctx, op.Task, verdict, comments)
	case OpAskOwner:
		_, err = s.client(op.Actor).AskOwner(ctx, op.Task, "which way?", "a", "b")
	default:
		return s.BoardSystem.Do(ctx, op)
	}
	var ce *client.Error
	if errors.As(err, &ce) && ce.Code != "" {
		err = &model.Error{Code: ce.Code, Message: ce.Message}
	}
	return Result{Err: err}
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

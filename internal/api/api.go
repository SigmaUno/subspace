package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type API struct {
	B          *board.Board
	Hub        events.Hub
	OwnerToken string
	Agents     auth.AgentLookup
	Log        *slog.Logger
	PingEvery  time.Duration
}

type errBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (a *API) fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	if e.Code == "internal" && a.Log != nil {
		a.Log.Error("api error", "err", err)
	}
	var b errBody
	b.Error.Code, b.Error.Message = e.Code, e.Message
	writeJSON(w, e.HTTPStatus(), b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decode(r *http.Request, v any) error {
	b, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err != nil {
		return model.BadRequest("request body too large or unreadable")
	}
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return model.BadRequest("invalid JSON body")
	}
	return nil
}

const maxListLimit = 1000

func qint(r *http.Request, name string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(name))
	return n
}

func limitq(r *http.Request) int {
	n := qint(r, "limit")
	if n > maxListLimit {
		return maxListLimit
	}
	return n
}

func qint64(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	return n
}

func (a *API) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		const p = "Bearer "
		hd := r.Header.Get("Authorization")
		if len(hd) <= len(p) || hd[:len(p)] != p {
			w.Header().Set("WWW-Authenticate", "Bearer")
			a.fail(w, model.Errf("unauthorized", "missing bearer token"))
			return
		}
		tok := hd[len(p):]
		if !auth.OwnerMatch(tok, a.OwnerToken) {
			if a.Agents != nil {
				if ag, _ := a.Agents.LookupToken(tok); ag != nil {
					a.fail(w, model.Forbidden("agent tokens are not valid on /api/v1"))
					return
				}
			}
			w.Header().Set("WWW-Authenticate", "Bearer")
			a.fail(w, model.Errf("unauthorized", "invalid token"))
			return
		}
		h(w, r)
	}
}

func (a *API) do(status int, f func(r *http.Request) (any, error)) http.HandlerFunc {
	return a.guard(func(w http.ResponseWriter, r *http.Request) {
		v, err := f(r)
		if err != nil {
			a.fail(w, err)
			return
		}
		writeJSON(w, status, v)
	})
}

func (a *API) Register(mux *http.ServeMux) {
	B := a.B
	m := func(p string, h http.HandlerFunc) { mux.HandleFunc(p, h) }

	m("GET /api/v1/state", a.do(200, func(r *http.Request) (any, error) { return B.State(r.Context()) }))
	m("POST /api/v1/pause-all", a.do(200, func(r *http.Request) (any, error) {
		var in struct{ Reason string }
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return map[string]any{"paused": true}, B.PauseAll(r.Context(), in.Reason)
	}))
	m("POST /api/v1/resume-all", a.do(200, func(r *http.Request) (any, error) {
		return map[string]any{"paused": false}, B.ResumeAll(r.Context())
	}))
	m("GET /api/v1/charter", a.do(200, func(r *http.Request) (any, error) { return B.GetCharter(r.Context()) }))
	m("PUT /api/v1/charter", a.do(200, func(r *http.Request) (any, error) {
		var in struct{ Text string }
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return B.SetCharter(r.Context(), in.Text)
	}))

	m("POST /api/v1/briefs", a.do(201, func(r *http.Request) (any, error) {
		var in board.BriefInput
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return B.CreateBrief(r.Context(), in)
	}))
	m("GET /api/v1/briefs", a.do(200, func(r *http.Request) (any, error) {
		bs, err := B.ListBriefs(r.Context(), r.URL.Query().Get("status"))
		return map[string]any{"briefs": bs}, err
	}))
	m("GET /api/v1/briefs/{id}", a.do(200, func(r *http.Request) (any, error) { return B.GetBrief(r.Context(), r.PathValue("id")) }))
	m("PATCH /api/v1/briefs/{id}", a.do(200, func(r *http.Request) (any, error) {
		var in board.BriefPatch
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return B.UpdateBrief(r.Context(), r.PathValue("id"), in)
	}))
	m("GET /api/v1/briefs/{id}/board", a.do(200, func(r *http.Request) (any, error) { return B.Board(r.Context(), r.PathValue("id")) }))

	m("GET /api/v1/plans", a.do(200, func(r *http.Request) (any, error) {
		ps, err := B.ListPlans(r.Context(), r.URL.Query().Get("brief_id"), r.URL.Query().Get("status"))
		return map[string]any{"plans": ps}, err
	}))
	m("GET /api/v1/plans/{id}", a.do(200, func(r *http.Request) (any, error) { return B.GetPlan(r.Context(), r.PathValue("id")) }))
	m("POST /api/v1/plans/{id}/approve", a.do(200, func(r *http.Request) (any, error) {
		var in struct{ Note string }
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return B.ApprovePlan(r.Context(), r.PathValue("id"), in.Note)
	}))
	m("POST /api/v1/plans/{id}/reject", a.do(200, func(r *http.Request) (any, error) {
		var in struct{ Reason string }
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return B.RejectPlan(r.Context(), r.PathValue("id"), in.Reason)
	}))

	m("GET /api/v1/tasks", a.do(200, func(r *http.Request) (any, error) {
		q := r.URL.Query()
		ts, err := B.ListTasks(r.Context(), board.TaskQuery{BriefID: q.Get("brief_id"), Status: q.Get("status"), Assignee: q.Get("assignee"), Role: q.Get("role"), Limit: limitq(r)})
		return map[string]any{"tasks": ts}, err
	}))
	m("GET /api/v1/tasks/{id}", a.do(200, func(r *http.Request) (any, error) { return B.GetTask(r.Context(), model.Owner, r.PathValue("id")) }))
	m("PATCH /api/v1/tasks/{id}", a.do(200, func(r *http.Request) (any, error) {
		var in struct {
			Budget model.Budget `json:"budget"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return B.SetTaskBudget(r.Context(), r.PathValue("id"), in.Budget)
	}))
	m("POST /api/v1/tasks/{id}/done", a.do(200, func(r *http.Request) (any, error) {
		var in struct{ Note string }
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return B.MarkDone(r.Context(), r.PathValue("id"), in.Note)
	}))
	m("POST /api/v1/tasks/{id}/cancel", a.do(200, func(r *http.Request) (any, error) {
		var in struct{ Reason string }
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return B.Cancel(r.Context(), r.PathValue("id"), in.Reason)
	}))
	m("POST /api/v1/tasks/{id}/reopen", a.do(200, func(r *http.Request) (any, error) {
		var in struct{ Status, Note string }
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return B.Reopen(r.Context(), r.PathValue("id"), in.Status, in.Note)
	}))

	m("GET /api/v1/agents", a.do(200, func(r *http.Request) (any, error) {
		as, err := B.ListAgents(r.Context(), r.URL.Query().Get("role"), r.URL.Query().Get("status"))
		return map[string]any{"agents": as}, err
	}))
	m("GET /api/v1/agents/{id}", a.do(200, func(r *http.Request) (any, error) { return B.GetAgent(r.Context(), r.PathValue("id")) }))
	m("POST /api/v1/agents", a.do(201, func(r *http.Request) (any, error) {
		var in board.AgentInput
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return B.RegisterAgent(r.Context(), in)
	}))
	m("POST /api/v1/agents/bulk", a.do(201, func(r *http.Request) (any, error) {
		var in board.BulkInput
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		rs, err := B.RegisterBulk(r.Context(), in)
		return map[string]any{"agents": rs}, err
	}))
	m("GET /api/v1/agents/{id}/connect", a.do(200, func(r *http.Request) (any, error) { return B.Connect(r.Context(), r.PathValue("id")) }))
	m("POST /api/v1/agents/{id}/rotate-token", a.do(200, func(r *http.Request) (any, error) { return B.RotateToken(r.Context(), r.PathValue("id")) }))
	m("POST /api/v1/agents/{id}/revoke", a.do(200, func(r *http.Request) (any, error) {
		var in struct{ Reason string }
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return B.RevokeAgent(r.Context(), r.PathValue("id"), in.Reason)
	}))
	m("POST /api/v1/agents/{id}/resume", a.do(200, func(r *http.Request) (any, error) { return B.ResumeAgent(r.Context(), r.PathValue("id")) }))

	m("GET /api/v1/questions", a.do(200, func(r *http.Request) (any, error) {
		qs, err := B.ListQuestions(r.Context(), r.URL.Query().Get("status"))
		return map[string]any{"questions": qs}, err
	}))
	m("POST /api/v1/questions/{id}/answer", a.do(200, func(r *http.Request) (any, error) {
		var in struct{ Answer string }
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return B.Answer(r.Context(), r.PathValue("id"), in.Answer)
	}))

	m("GET /api/v1/threads/{kind}/{id}/messages", a.do(200, func(r *http.Request) (any, error) {
		ms, err := B.OwnerThread(r.Context(), r.PathValue("kind"), r.PathValue("id"), qint64(r, "since"), limitq(r))
		return map[string]any{"messages": ms}, err
	}))
	m("POST /api/v1/threads/{kind}/{id}/messages", a.do(201, func(r *http.Request) (any, error) {
		var in struct{ Body string }
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return B.OwnerReply(r.Context(), r.PathValue("kind"), r.PathValue("id"), in.Body)
	}))

	m("GET /api/v1/capacity", a.do(200, func(r *http.Request) (any, error) { return B.Capacity(r.Context()) }))
	m("PUT /api/v1/capacity", a.do(200, func(r *http.Request) (any, error) {
		var in capacity.Config
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return B.SetCapacity(r.Context(), in)
	}))
	m("GET /api/v1/spawns", a.do(200, func(r *http.Request) (any, error) {
		sp, err := B.ListSpawns(r.Context(), r.URL.Query().Get("brief_id"))
		return map[string]any{"spawns": sp}, err
	}))
	m("GET /api/v1/audit", a.do(200, func(r *http.Request) (any, error) {
		q := r.URL.Query()
		ev, err := B.ListAudit(r.Context(), store.AuditFilter{AgentID: q.Get("agent_id"), TaskID: q.Get("task_id"), Tool: q.Get("tool"), SinceID: qint64(r, "since"), Limit: limitq(r)})
		return map[string]any{"events": ev}, err
	}))

	m("GET /api/v1/events", a.guard(events.NewSSEHandler(a.Hub, events.TopicsFromQuery, a.PingEvery).ServeHTTP))

	mux.HandleFunc("/api/v1/", a.guard(func(w http.ResponseWriter, r *http.Request) {
		a.fail(w, model.NotFound("no such endpoint"))
	}))
}

func (a *API) Mount(mux *http.ServeMux, pattern string, h http.Handler) {
	mux.HandleFunc(pattern, a.guard(http.StripPrefix("/api/v1", h).ServeHTTP))
}

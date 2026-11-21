package policy

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/SigmaUno/subspace/internal/httpx"
	"github.com/SigmaUno/subspace/internal/model"
)

const maxRequest = 256 << 10

func Handler(s *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /policies", s.handleList)
	mux.HandleFunc("POST /policies", s.handleCreate)
	mux.HandleFunc("GET /policies/facts", s.handleFacts)
	mux.HandleFunc("POST /policies/lint", s.handleLint)
	mux.HandleFunc("POST /policies/evaluate", s.handleEvaluate)
	mux.HandleFunc("GET /policies/decisions", s.handleDecisions)
	mux.HandleFunc("GET /policy-holds", s.handleHolds)
	mux.HandleFunc("GET /policy-holds/{id}", s.handleHold)
	mux.HandleFunc("POST /policy-holds/{id}/approve", s.handleDecide(true))
	mux.HandleFunc("POST /policy-holds/{id}/reject", s.handleDecide(false))
	mux.HandleFunc("GET /policies/{id}", s.handleGet)
	mux.HandleFunc("PUT /policies/{id}", s.handleReplace)
	mux.HandleFunc("DELETE /policies/{id}", s.handleDelete)
	mux.HandleFunc("GET /policies/{id}/versions", s.handleVersions)
	return mux
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequest))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil || dec.More() {
		return model.BadRequest("invalid JSON body")
	}
	return nil
}

func reply(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, status, v)
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	ps, err := s.List(r.Context())
	reply(w, http.StatusOK, map[string]any{"policies": ps}, err)
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	var d Document
	if err := decode(w, r, &d); err != nil {
		httpx.WriteError(w, err)
		return
	}
	p, err := s.Create(r.Context(), d)
	reply(w, http.StatusCreated, p, err)
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	p, err := s.Get(r.Context(), r.PathValue("id"))
	reply(w, http.StatusOK, p, err)
}

func (s *Service) handleReplace(w http.ResponseWriter, r *http.Request) {
	var d Document
	if err := decode(w, r, &d); err != nil {
		httpx.WriteError(w, err)
		return
	}
	p, err := s.Replace(r.Context(), r.PathValue("id"), d)
	reply(w, http.StatusOK, p, err)
}

func (s *Service) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Delete(r.Context(), r.PathValue("id")); err != nil {
		httpx.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleVersions(w http.ResponseWriter, r *http.Request) {
	vs, err := s.Versions(r.Context(), r.PathValue("id"))
	reply(w, http.StatusOK, map[string]any{"versions": vs}, err)
}

func (s *Service) handleFacts(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"facts": FactList(), "actions": Actions, "effects": Effects})
}

func (s *Service) handleLint(w http.ResponseWriter, r *http.Request) {
	var d Document
	if err := decode(w, r, &d); err != nil {
		httpx.WriteError(w, err)
		return
	}
	fs := Lint(d)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": !HasErrors(fs), "findings": fs})
}

func (s *Service) handleEvaluate(w http.ResponseWriter, r *http.Request) {
	var in DryRunInput
	if err := decode(w, r, &in); err != nil {
		httpx.WriteError(w, err)
		return
	}
	res, err := s.DryRun(r.Context(), in)
	reply(w, http.StatusOK, res, err)
}

func (s *Service) handleDecisions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := intParam(q.Get("limit"), "limit", defaultDecisions, maxList)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	before, err := intParam(q.Get("before"), "before", 0, 1<<62)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	f := DecisionFilter{Outcome: q.Get("outcome"), Action: q.Get("action"), TaskID: q.Get("task_id"), Before: int64(before), Limit: limit}
	ds, err := s.Decisions(r.Context(), f)
	reply(w, http.StatusOK, map[string]any{"decisions": ds}, err)
}

func (s *Service) handleHolds(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := intParam(q.Get("limit"), "limit", defaultDecisions, maxList)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	switch st := q.Get("status"); st {
	case "", HoldPending, HoldApproved, HoldConsumed, HoldRejected, HoldExpired:
		hs, err := s.Holds(r.Context(), st, limit)
		reply(w, http.StatusOK, map[string]any{"holds": hs}, err)
	default:
		httpx.WriteError(w, model.BadRequest("status must be pending, approved, consumed, rejected or expired"))
	}
}

func (s *Service) handleHold(w http.ResponseWriter, r *http.Request) {
	h, err := s.GetHold(r.Context(), r.PathValue("id"))
	reply(w, http.StatusOK, h, err)
}

type decideInput struct {
	Note string `json:"note"`
}

func (s *Service) handleDecide(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in decideInput
		if r.ContentLength != 0 {
			if err := decode(w, r, &in); err != nil {
				httpx.WriteError(w, err)
				return
			}
		}
		if len(in.Note) > MaxMessage {
			httpx.WriteError(w, model.BadRequest("note is longer than %d bytes", MaxMessage))
			return
		}
		var (
			h   *Hold
			err error
		)
		if approve {
			h, err = s.Approve(r.Context(), r.PathValue("id"), in.Note)
		} else {
			h, err = s.Reject(r.Context(), r.PathValue("id"), in.Note)
		}
		reply(w, http.StatusOK, h, err)
	}
}

func intParam(v, name string, def, max int) (int, error) {
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > max {
		return 0, model.BadRequest("%s must be an integer between 1 and %d", name, max)
	}
	return n, nil
}

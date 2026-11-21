package quota

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/SigmaUno/subspace/internal/httpx"
	"github.com/SigmaUno/subspace/internal/model"
)

const (
	maxIDs  = 500
	maxBody = 1 << 20
)

type Status struct {
	AgentID string `json:"agent_id"`
	Quota   Quota  `json:"quota"`
	Usage   Today  `json:"quota_usage"`
}

type bulkRequest struct {
	IDs   []string `json:"ids"`
	Quota *Quota   `json:"quota"`
}

type bulkError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type bulkResult struct {
	ID    string     `json:"id"`
	OK    bool       `json:"ok"`
	Error *bulkError `json:"error,omitempty"`
}

func Handler(s *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /agents/{id}/quota", func(w http.ResponseWriter, r *http.Request) {
		var lim Quota
		if err := httpx.DecodeStrict(w, r, maxBody, &lim); err != nil {
			httpx.WriteError(w, err)
			return
		}
		id := r.PathValue("id")
		if err := s.Set(r.Context(), id, lim); err != nil {
			httpx.WriteError(w, err)
			return
		}
		sn, err := s.Snapshot(r.Context())
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		got, today := sn.Of(id)
		httpx.WriteJSON(w, http.StatusOK, Status{AgentID: id, Quota: got, Usage: today})
	})
	mux.HandleFunc("POST /agents/quota-bulk", s.handleBulk)
	return mux
}

func (s *Service) handleBulk(w http.ResponseWriter, r *http.Request) {
	var req bulkRequest
	if err := httpx.DecodeStrict(w, r, maxBody, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}
	switch {
	case len(req.IDs) == 0 || len(req.IDs) > maxIDs:
		httpx.WriteError(w, model.BadRequest("ids must hold 1 to %d agent ids", maxIDs))
		return
	case slices.Contains(req.IDs, ""):
		httpx.WriteError(w, model.BadRequest("ids must not contain empty values"))
		return
	case req.Quota == nil:
		httpx.WriteError(w, model.BadRequest("quota is required"))
		return
	}
	if err := req.Quota.Validate(); err != nil {
		httpx.WriteError(w, err)
		return
	}
	out := struct {
		Results   []bulkResult `json:"results"`
		Succeeded int          `json:"succeeded"`
		Failed    int          `json:"failed"`
	}{Results: []bulkResult{}}
	seen := map[string]bool{}
	for _, id := range req.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		res := bulkResult{ID: id, OK: true}
		if err := s.Set(r.Context(), id, *req.Quota); err != nil {
			e := model.AsError(err)
			res = bulkResult{ID: id, Error: &bulkError{Code: e.Code, Message: e.Message}}
			out.Failed++
		} else {
			out.Succeeded++
		}
		out.Results = append(out.Results, res)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func Enrich(s *Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !agentRead(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		httpx.Rewrite(w, r, next, func(code int, body []byte) []byte {
			if code != http.StatusOK {
				return body
			}
			if sn, err := s.Snapshot(r.Context()); err == nil {
				if out, ok := enrich(body, sn); ok {
					return out
				}
			}
			return body
		})
	})
}

func agentRead(path string) bool {
	if path == "/api/v1/agents" {
		return true
	}
	rest, ok := strings.CutPrefix(path, "/api/v1/agents/")
	return ok && rest != "" && !strings.Contains(rest, "/")
}

func enrich(body []byte, sn *Snapshot) ([]byte, bool) {
	var doc map[string]json.RawMessage
	if json.Unmarshal(body, &doc) != nil {
		return nil, false
	}
	add := func(raw json.RawMessage) (json.RawMessage, bool) {
		var agent map[string]json.RawMessage
		if json.Unmarshal(raw, &agent) != nil {
			return nil, false
		}
		var id string
		if json.Unmarshal(agent["id"], &id) != nil {
			return nil, false
		}
		lim, today := sn.Of(id)
		agent["quota"], _ = json.Marshal(lim)
		agent["quota_usage"], _ = json.Marshal(today)
		out, err := json.Marshal(agent)
		return out, err == nil
	}
	list, ok := doc["agents"]
	if !ok {
		single, ok := add(body)
		return append(single, '\n'), ok
	}
	var agents []json.RawMessage
	if json.Unmarshal(list, &agents) != nil {
		return nil, false
	}
	for i, a := range agents {
		enriched, ok := add(a)
		if !ok {
			return nil, false
		}
		agents[i] = enriched
	}
	doc["agents"], _ = json.Marshal(agents)
	out, err := json.Marshal(doc)
	return append(out, '\n'), err == nil
}

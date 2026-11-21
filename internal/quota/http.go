package quota

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

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
		if !decode(w, r, &lim) {
			return
		}
		id := r.PathValue("id")
		if err := s.Set(r.Context(), id, lim); err != nil {
			writeError(w, err)
			return
		}
		sn, err := s.Snapshot(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		got, today := sn.Of(id)
		writeJSON(w, http.StatusOK, Status{AgentID: id, Quota: got, Usage: today})
	})
	mux.HandleFunc("POST /agents/quota-bulk", func(w http.ResponseWriter, r *http.Request) {
		var req bulkRequest
		if !decode(w, r, &req) {
			return
		}
		switch {
		case len(req.IDs) == 0 || len(req.IDs) > maxIDs:
			writeError(w, model.BadRequest("ids must hold 1 to %d agent ids", maxIDs))
			return
		case slices.Contains(req.IDs, ""):
			writeError(w, model.BadRequest("ids must not contain empty values"))
			return
		case req.Quota == nil:
			writeError(w, model.BadRequest("quota is required"))
			return
		}
		if err := req.Quota.Validate(); err != nil {
			writeError(w, err)
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
		writeJSON(w, http.StatusOK, out)
	})
	return mux
}

func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		writeError(w, model.BadRequest("invalid body: %v", err))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	writeJSON(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

type capture struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (c *capture) Header() http.Header         { return c.header }
func (c *capture) WriteHeader(code int)        { c.code = code }
func (c *capture) Write(p []byte) (int, error) { return c.body.Write(p) }

func Enrich(s *Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !agentRead(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		c := &capture{header: http.Header{}, code: http.StatusOK}
		next.ServeHTTP(c, r)
		body := c.body.Bytes()
		if c.code == http.StatusOK {
			if sn, err := s.Snapshot(r.Context()); err == nil {
				if out, ok := enrich(body, sn); ok {
					body = out
				}
			}
		}
		for k, v := range c.header {
			w.Header()[k] = v
		}
		w.Header().Del("Content-Length")
		w.WriteHeader(c.code)
		_, _ = w.Write(body)
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

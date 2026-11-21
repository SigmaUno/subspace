package profiles

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
)

const maxBody = 1 << 20

type handler struct {
	s     *Store
	inUse InUse
	log   *slog.Logger
}

func Handler(s *Store, inUse InUse, log *slog.Logger) http.Handler {
	h := &handler{s: s, inUse: inUse, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /agent-profiles", h.list)
	mux.HandleFunc("POST /agent-profiles", h.create)
	mux.HandleFunc("GET /agent-profiles/{name}", h.get)
	mux.HandleFunc("PUT /agent-profiles/{name}", h.update)
	mux.HandleFunc("DELETE /agent-profiles/{name}", h.remove)
	return mux
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	ps, err := h.s.List(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusOK, map[string]any{"profiles": ps})
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	p, err := h.s.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusOK, p)
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var in Profile
	if !h.decode(w, r, &in) {
		return
	}
	p, err := h.s.Create(r.Context(), in)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusCreated, p)
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	var in Profile
	if !h.decode(w, r, &in) {
		return
	}
	name := r.PathValue("name")
	if in.Name != "" && in.Name != name {
		h.fail(w, model.BadRequest("name cannot be changed"))
		return
	}
	p, err := h.s.Update(r.Context(), name, in)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusOK, p)
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.s.Delete(r.Context(), r.PathValue("name"), h.inUse); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		h.fail(w, model.BadRequest("invalid JSON body"))
		return false
	}
	return true
}

func (h *handler) fail(w http.ResponseWriter, err error) {
	writeError(w, h.log, err)
}

func (h *handler) write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, log *slog.Logger, err error) {
	e := model.AsError(err)
	if e.Code == "internal" && log != nil {
		log.Error("agent profiles", "err", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.HTTPStatus())
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

func isRegistration(r *http.Request) (bulk, ok bool) {
	if r.Method != http.MethodPost {
		return false, false
	}
	switch r.URL.Path {
	case "/api/v1/agents":
		return false, true
	case "/api/v1/agents/bulk":
		return true, true
	}
	return false, false
}

func Expand(next http.Handler, s *Store, owner func(*http.Request) bool, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bulk, ok := isRegistration(r)
		if !ok || r.Body == nil || !owner(r) {
			next.ServeHTTP(w, r)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		_ = r.Body.Close()
		restore := func(b []byte) {
			r.Body = io.NopCloser(bytes.NewReader(b))
			r.ContentLength = int64(len(b))
			r.Header.Del("Content-Length")
		}
		restore(raw)
		var fields Fields
		if err != nil || len(raw) > maxBody || json.Unmarshal(raw, &fields) != nil {
			next.ServeHTTP(w, r)
			return
		}
		ref, present := fields["profile"]
		if !present {
			next.ServeHTTP(w, r)
			return
		}
		out, err := expandBody(r, s, fields, ref, bulk)
		if err != nil {
			writeError(w, log, err)
			return
		}
		restore(out)
		next.ServeHTTP(w, r)
	})
}

func expandBody(r *http.Request, s *Store, fields Fields, ref json.RawMessage, bulk bool) ([]byte, error) {
	delete(fields, "profile")
	if string(ref) == "null" {
		return json.Marshal(fields)
	}
	var name string
	if json.Unmarshal(ref, &name) != nil || name == "" {
		return nil, model.BadRequest("profile must be a profile name")
	}
	p, err := s.Get(r.Context(), name)
	if err != nil {
		if model.AsError(err).Code == "not_found" {
			return nil, model.BadRequest("unknown profile %q", name)
		}
		return nil, err
	}
	p.ApplyFields(fields, bulk)
	return json.Marshal(fields)
}

type Fields map[string]json.RawMessage

func (f Fields) Blank(key string) bool {
	raw := f[key]
	if len(raw) == 0 || string(raw) == "null" {
		return true
	}
	var s string
	return json.Unmarshal(raw, &s) == nil && s == ""
}

func (f Fields) Text(key string) string {
	var s string
	_ = json.Unmarshal(f[key], &s)
	return s
}

func (f Fields) Fill(key, value string) {
	if value == "" || !f.Blank(key) {
		return
	}
	raw, _ := json.Marshal(value)
	f[key] = raw
}

func (f Fields) zero(key string) bool {
	raw, ok := f[key]
	if !ok || string(raw) == "null" {
		return true
	}
	var n int
	return json.Unmarshal(raw, &n) == nil && n == 0
}

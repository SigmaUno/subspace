package callback

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/store"
)

const maxBody = 1 << 20

type Setter interface {
	Set(ctx context.Context, agentID string, t Target) error
}

type Registrar struct {
	Policy     Policy
	Store      Setter
	OwnerToken string
	Log        *slog.Logger
	Activated  func()
}

type capture struct {
	h    http.Header
	code int
	buf  bytes.Buffer
}

func (c *capture) Header() http.Header         { return c.h }
func (c *capture) WriteHeader(code int)        { c.code = code }
func (c *capture) Write(p []byte) (int, error) { return c.buf.Write(p) }

func agentRoute(r *http.Request) (id string, rotate, ok bool) {
	if r.Method != http.MethodPost {
		return "", false, false
	}
	if r.URL.Path == "/api/v1/agents" {
		return "", false, true
	}
	rest, found := strings.CutPrefix(r.URL.Path, "/api/v1/agents/")
	if !found {
		return "", false, false
	}
	id, found = strings.CutSuffix(rest, "/rotate-token")
	if !found || id == "" || strings.Contains(id, "/") {
		return "", false, false
	}
	return id, true, true
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func (g *Registrar) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, rotate, ok := agentRoute(r)
		if !ok || r.Body == nil || !g.isOwner(r) {
			next.ServeHTTP(w, r)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if err != nil || len(raw) > maxBody {
			next.ServeHTTP(w, r)
			return
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			next.ServeHTTP(w, r)
			return
		}
		field, present := fields["callback_url"]
		if !present || string(field) == "null" {
			next.ServeHTTP(w, r)
			return
		}
		var url string
		if json.Unmarshal(field, &url) != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "callback_url must be a string")
			return
		}
		if url == "" && !rotate {
			next.ServeHTTP(w, r)
			return
		}
		if url != "" {
			if err := g.Policy.Validate(r.Context(), url); err != nil {
				writeError(w, http.StatusBadRequest, "bad_request", err.Error())
				return
			}
		}
		g.apply(w, r, next, id, url)
	})
}

func (g *Registrar) isOwner(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && auth.OwnerMatch(tok, g.OwnerToken)
}

func (g *Registrar) apply(w http.ResponseWriter, r *http.Request, next http.Handler, id, url string) {
	c := &capture{h: http.Header{}, code: http.StatusOK}
	next.ServeHTTP(c, r)
	body := c.buf.Bytes()
	if c.code >= 200 && c.code < 300 {
		body = g.attach(r.Context(), body, id, url)
	}
	for k, v := range c.h {
		w.Header()[k] = v
	}
	w.Header().Del("Content-Length")
	w.WriteHeader(c.code)
	_, _ = w.Write(body) //nolint:gosec // body is the handler's own JSON response
}

func (g *Registrar) attach(ctx context.Context, body []byte, id, url string) []byte {
	var resp map[string]any
	if json.Unmarshal(body, &resp) != nil {
		return body
	}
	if agent, ok := resp["agent"].(map[string]any); ok {
		if v, ok := agent["id"].(string); ok {
			id = v
		}
	}
	t := Target{URL: url}
	if url != "" {
		secret, err := NewSecret()
		if err != nil {
			return g.failed(resp, body, err)
		}
		t.Secret = secret
	}
	if err := g.Store.Set(ctx, id, t); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return body
		}
		return g.failed(resp, body, err)
	}
	resp["callback"] = nil
	if url != "" {
		resp["callback"] = map[string]any{"url": url, "secret": t.Secret}
		if g.Activated != nil {
			g.Activated()
		}
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return body
	}
	return append(out, '\n')
}

func (g *Registrar) failed(resp map[string]any, body []byte, err error) []byte {
	if g.Log != nil {
		g.Log.Error("callback registration", "err", err)
	}
	resp["callback"] = map[string]any{"error": "callback could not be stored; rotate the token again to retry"}
	out, merr := json.Marshal(resp)
	if merr != nil {
		return body
	}
	return append(out, '\n')
}

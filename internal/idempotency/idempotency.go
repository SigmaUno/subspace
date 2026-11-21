package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/model"
)

const (
	Header  = "Idempotency-Key"
	Replay  = "Idempotent-Replayed"
	TTL     = 24 * time.Hour
	MaxKey  = 255
	maxBody = 1 << 20
)

var tools = map[string]bool{
	"claim_task": true, "release_task": true, "submit": true, "review": true, "update_status": true,
	"report_tests": true, "ask_owner": true, "post_message": true, "add_note": true, "assign": true,
	"reassign": true, "propose_plan": true, "create_task": true, "create_task_from_template": true, "request_spawn": true,
}

var transient = map[string]bool{"internal": true, "agent_paused": true, "rate_limited": true}

func Applies(tool string) bool { return tools[tool] }

type Record struct {
	Tool      string
	ArgsHash  string
	Result    json.RawMessage
	ExpiresAt time.Time
}

type Store interface {
	GetIdempotency(ctx context.Context, agentID, key string, now time.Time) (*Record, error)
	PutIdempotency(ctx context.Context, agentID, key string, r *Record, now time.Time) error
}

type Options struct {
	Store  Store
	Agents auth.AgentLookup
	Now    func() time.Time
	Log    *slog.Logger
}

type handler struct {
	next  http.Handler
	opts  Options
	mu    sync.Mutex
	locks map[string]*keyLock
}

type keyLock struct {
	mu   sync.Mutex
	refs int
}

func Wrap(next http.Handler, o Options) http.Handler {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &handler{next: next, opts: o, locks: map[string]*keyLock{}}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      struct {
			Key string `json:"idempotencyKey"`
		} `json:"_meta"`
	} `json:"params"`
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/mcp" {
		h.next.ServeHTTP(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(body), r.Body), r.Body}
	if err != nil || len(body) > maxBody {
		h.next.ServeHTTP(w, r)
		return
	}
	var req request
	if json.Unmarshal(body, &req) != nil || req.Method != "tools/call" || len(req.ID) == 0 || !Applies(req.Params.Name) {
		h.next.ServeHTTP(w, r)
		return
	}
	key := req.Params.Meta.Key
	if key == "" {
		key = r.Header.Get(Header)
	}
	if key == "" {
		h.next.ServeHTTP(w, r)
		return
	}
	agent := h.agent(r)
	if agent == nil {
		h.next.ServeHTTP(w, r)
		return
	}
	if len(key) > MaxKey {
		writeRPC(w, req.ID, toolError("bad_request", "idempotency key longer than 255 bytes"), false)
		return
	}
	sum := ArgsHash(req.Params.Arguments)
	unlock := h.lock(agent.ID + "\x00" + key)
	defer unlock()
	ctx := r.Context()
	rec, err := h.opts.Store.GetIdempotency(ctx, agent.ID, key, h.opts.Now())
	if err != nil {
		h.opts.Log.Error("idempotency lookup", "err", err)
		writeRPC(w, req.ID, toolError("internal", "internal error"), false)
		return
	}
	if rec != nil {
		if rec.Tool != req.Params.Name || rec.ArgsHash != sum {
			writeRPC(w, req.ID, toolError("idempotency_conflict", "idempotency key was already used for a different call"), false)
			return
		}
		writeRPC(w, req.ID, rec.Result, true)
		return
	}
	rw := &recorder{header: http.Header{}, code: http.StatusOK}
	h.next.ServeHTTP(rw, r)
	if result := storable(rw); result != nil {
		now := h.opts.Now()
		rec := &Record{Tool: req.Params.Name, ArgsHash: sum, Result: result, ExpiresAt: now.Add(TTL)}
		if err := h.opts.Store.PutIdempotency(context.WithoutCancel(ctx), agent.ID, key, rec, now); err != nil {
			h.opts.Log.Error("idempotency store", "err", err)
		}
	}
	for k, v := range rw.header {
		w.Header()[k] = v
	}
	w.WriteHeader(rw.code)
	_, _ = w.Write(rw.body.Bytes())
}

func (h *handler) agent(r *http.Request) *model.Agent {
	const p = "Bearer "
	hd := r.Header.Get("Authorization")
	if h.opts.Agents == nil || len(hd) <= len(p) || hd[:len(p)] != p {
		return nil
	}
	a, err := h.opts.Agents.LookupToken(hd[len(p):])
	if err != nil || a == nil || a.Status == model.AgentRevoked {
		return nil
	}
	return a
}

func (h *handler) lock(k string) func() {
	h.mu.Lock()
	l := h.locks[k]
	if l == nil {
		l = &keyLock{}
		h.locks[k] = l
	}
	l.refs++
	h.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		h.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(h.locks, k)
		}
		h.mu.Unlock()
	}
}

func ArgsHash(raw json.RawMessage) string {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&v) != nil || v == nil {
		v = map[string]any{}
	}
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func storable(rw *recorder) json.RawMessage {
	if rw.code != http.StatusOK {
		return nil
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(rw.body.Bytes(), &resp) != nil || len(resp.Error) > 0 || len(resp.Result) == 0 {
		return nil
	}
	var res struct {
		IsError           bool `json:"isError"`
		StructuredContent struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"structuredContent"`
	}
	if json.Unmarshal(resp.Result, &res) != nil {
		return nil
	}
	if res.IsError && transient[res.StructuredContent.Error.Code] {
		return nil
	}
	return resp.Result
}

func toolError(code, msg string) json.RawMessage {
	sc, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": msg}})
	b, _ := json.Marshal(map[string]any{
		"content":           []map[string]string{{"type": "text", "text": string(sc)}},
		"structuredContent": json.RawMessage(sc),
		"isError":           true,
	})
	return b
}

func writeRPC(w http.ResponseWriter, id, result json.RawMessage, replayed bool) {
	w.Header().Set("Content-Type", "application/json")
	if replayed {
		w.Header().Set(Replay, "true")
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

type recorder struct {
	header http.Header
	code   int
	body   bytes.Buffer
	wrote  bool
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		r.code, r.wrote = code, true
	}
}

func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.body.Write(b)
}

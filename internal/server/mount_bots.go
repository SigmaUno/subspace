package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/spawner"
	"github.com/SigmaUno/subspace/internal/store"
)

const botsMaxBody = 1 << 20

var botKinds = map[string]struct{ kind, provider string }{
	"claude-code":        {model.KindClaudeCode, "anthropic"},
	"codex":              {model.KindCodex, "openai"},
	"opencode":           {model.KindOpenCode, "deepseek"},
	"opencode-anthropic": {model.KindOpenCode, "anthropic"},
	"opencode-deepseek":  {model.KindOpenCode, "deepseek"},
	"other":              {model.KindOther, "other"},
}

type botStopper interface {
	Stop(ctx context.Context, agentID string) error
}

type botsHandler struct {
	b      *board.Board
	stop   botStopper
	window time.Duration
	log    *slog.Logger
}

type liveBot struct {
	*model.Agent
	Live bool `json:"live"`
}

type botReason struct {
	Reason string `json:"reason"`
}

type botInput struct {
	Name      string `json:"name"`
	Prefix    string `json:"prefix"`
	Count     int    `json:"count"`
	Role      string `json:"role"`
	Kind      string `json:"kind"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	ExpiresIn string `json:"expires_in"`
}

func init() {
	registerMount(func(d Deps) {
		window := 2 * d.Cfg.Lease
		if window <= 0 {
			window = 30 * time.Minute
		}
		if v, err := time.ParseDuration(os.Getenv("SUBSPACE_BOTS_LIVE_WINDOW")); err == nil && v > 0 {
			window = v
		}
		h := &botsHandler{b: d.Board, window: window, log: d.Log}
		h.stop, _ = d.Cfg.Spawner.(botStopper)
		mux := http.NewServeMux()
		mux.HandleFunc("GET /bots", h.list)
		mux.HandleFunc("POST /bots", h.create)
		mux.HandleFunc("POST /bots/bulk", h.bulk)
		mux.HandleFunc("POST /bots/spawn", h.spawn)
		mux.HandleFunc("GET /bots/{id}", h.get)
		mux.HandleFunc("DELETE /bots/{id}", h.remove)
		mux.HandleFunc("POST /bots/{id}/rotate-token", h.rotate)
		mux.HandleFunc("POST /bots/{id}/revoke", h.remove)
		mux.HandleFunc("POST /bots/{id}/pause", h.pause)
		mux.HandleFunc("POST /bots/{id}/resume", h.resume)
		mux.HandleFunc("POST /bots/{id}/kill", h.kill)
		d.API.Mount(d.Mux, "/api/v1/bots", mux)
		d.API.Mount(d.Mux, "/api/v1/bots/", mux)
	})
}

func (in *botInput) resolve() error {
	k, ok := botKinds[in.Kind]
	if !ok {
		return model.BadRequest("kind must be claude-code, codex, opencode, opencode-anthropic, opencode-deepseek or other")
	}
	if in.Provider == "" {
		in.Provider = k.provider
	}
	in.Kind = k.kind
	if in.Role == "" {
		in.Role = model.RoleWorker
	}
	return nil
}

func (h *botsHandler) live(a *model.Agent) liveBot {
	seen := a.LastSeenAt != nil && time.Since(*a.LastSeenAt) < h.window
	return liveBot{Agent: a, Live: a.Status == model.AgentActive && seen}
}

func (h *botsHandler) agent(ctx context.Context, id string) (*model.Agent, error) {
	a, err := h.b.GetAgent(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, model.NotFound("bot %s not found", id)
	}
	return a, err
}

func (h *botsHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	as, err := h.b.ListAgents(r.Context(), q.Get("role"), q.Get("status"))
	if err != nil {
		h.fail(w, err)
		return
	}
	bots := make([]liveBot, len(as))
	for i, a := range as {
		bots[i] = h.live(a)
	}
	h.write(w, http.StatusOK, map[string]any{"bots": bots})
}

func (h *botsHandler) get(w http.ResponseWriter, r *http.Request) {
	h.reply(w)(h.agent(r.Context(), r.PathValue("id")))
}

func (h *botsHandler) create(w http.ResponseWriter, r *http.Request) {
	var in botInput
	if !h.decode(w, r, &in) || !h.valid(w, in.resolve()) {
		return
	}
	reg, err := h.b.RegisterAgent(r.Context(), board.AgentInput{ID: in.Name, Role: in.Role, Kind: in.Kind, Model: in.Model, Provider: in.Provider, ExpiresIn: in.ExpiresIn})
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusCreated, map[string]any{"bot": reg.Agent, "connect": reg.Connect})
}

func (h *botsHandler) bulk(w http.ResponseWriter, r *http.Request) {
	var in botInput
	if !h.decode(w, r, &in) || !h.valid(w, in.resolve()) {
		return
	}
	regs, err := h.b.RegisterBulk(r.Context(), board.BulkInput{Prefix: in.Prefix, Count: in.Count, Role: in.Role, Kind: in.Kind, Model: in.Model, Provider: in.Provider, ExpiresIn: in.ExpiresIn})
	if err != nil {
		h.fail(w, err)
		return
	}
	out := make([]map[string]any, len(regs))
	for i, reg := range regs {
		out[i] = map[string]any{"bot": reg.Agent, "connect": reg.Connect}
	}
	h.write(w, http.StatusCreated, map[string]any{"bots": out})
}

func (h *botsHandler) spawn(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BriefID  string `json:"brief_id"`
		Kind     string `json:"kind"`
		Model    string `json:"model"`
		N        int    `json:"n"`
		Role     string `json:"role"`
		Provider string `json:"provider"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	k, ok := botKinds[in.Kind]
	if !ok || k.kind == model.KindOther {
		h.fail(w, model.BadRequest("kind must be claude-code, codex, opencode, opencode-anthropic or opencode-deepseek"))
		return
	}
	if in.Provider == "" {
		in.Provider = k.provider
	}
	if in.N == 0 {
		in.N = 1
	}
	sps, err := h.b.RequestSpawn(r.Context(), model.Owner, board.SpawnInput{
		BriefID: in.BriefID, Kind: k.kind, Model: in.Model, N: in.N, Role: in.Role, Provider: in.Provider,
	})
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusCreated, map[string]any{"spawns": sps})
}

func (h *botsHandler) rotate(w http.ResponseWriter, r *http.Request) {
	var in botInput
	if !h.decode(w, r, &in) {
		return
	}
	ttl, err := board.ParseExpiresIn(in.ExpiresIn)
	if err != nil {
		h.fail(w, err)
		return
	}
	reg, err := h.b.RotateTokenFor(r.Context(), r.PathValue("id"), ttl)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusOK, map[string]any{"bot": reg.Agent, "connect": reg.Connect})
}

func (h *botsHandler) remove(w http.ResponseWriter, r *http.Request) {
	var in botReason
	if r.Method != http.MethodDelete && !h.decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	a, err := h.agent(r.Context(), id)
	if err != nil {
		h.fail(w, err)
		return
	}
	if a.Status == model.AgentRevoked {
		h.fail(w, model.Conflict("bot %s is already revoked", id))
		return
	}
	h.reply(w)(h.b.RevokeAgent(r.Context(), id, in.Reason))
}

func (h *botsHandler) pause(w http.ResponseWriter, r *http.Request) {
	var in botReason
	if !h.decode(w, r, &in) {
		return
	}
	h.reply(w)(h.pauseBot(r.Context(), r.PathValue("id"), in.Reason, "paused by owner"))
}

func (h *botsHandler) pauseBot(ctx context.Context, id, reason, fallback string) (*model.Agent, error) {
	a, err := h.agent(ctx, id)
	if err != nil {
		return nil, err
	}
	if a.Status == model.AgentRevoked {
		return nil, model.Conflict("bot %s is revoked", id)
	}
	if reason == "" {
		reason = fallback
	}
	if err := h.b.PauseAgent(ctx, id, reason); err != nil {
		return nil, err
	}
	return h.agent(ctx, id)
}

func (h *botsHandler) resume(w http.ResponseWriter, r *http.Request) {
	h.reply(w)(h.b.ResumeAgent(r.Context(), r.PathValue("id")))
}

func (h *botsHandler) kill(w http.ResponseWriter, r *http.Request) {
	var in botReason
	if !h.decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	a, err := h.pauseBot(r.Context(), id, in.Reason, "killed by owner")
	if err != nil {
		h.fail(w, err)
		return
	}
	stopped := false
	if h.stop != nil {
		switch err := h.stop.Stop(r.Context(), id); {
		case errors.Is(err, spawner.ErrNotImplemented):
		case err != nil:
			h.log.Error("bot stop failed", "bot", id, "err", err)
			h.fail(w, model.Errf("internal", "bot %s paused but stop failed", id))
			return
		default:
			stopped = true
		}
	}
	h.write(w, http.StatusOK, map[string]any{"bot": h.live(a), "stopped": stopped})
}

func (h *botsHandler) reply(w http.ResponseWriter) func(*model.Agent, error) {
	return func(a *model.Agent, err error) {
		if err != nil {
			h.fail(w, err)
			return
		}
		h.write(w, http.StatusOK, map[string]any{"bot": h.live(a)})
	}
}

func (h *botsHandler) valid(w http.ResponseWriter, err error) bool {
	if err != nil {
		h.fail(w, err)
		return false
	}
	return true
}

func (h *botsHandler) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, botsMaxBody))
	if err != nil {
		h.fail(w, model.BadRequest("request body too large or unreadable"))
		return false
	}
	if len(raw) == 0 {
		return true
	}
	if err := json.Unmarshal(raw, v); err != nil {
		h.fail(w, model.BadRequest("invalid JSON body"))
		return false
	}
	return true
}

func (h *botsHandler) fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	if e.Code == "internal" {
		h.log.Error("bots", "err", err)
	}
	h.write(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

func (h *botsHandler) write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

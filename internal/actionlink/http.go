package actionlink

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/model"
)

const (
	maxForm    = 8 << 10
	cookiePfx  = "ssa_"
	snoozeFor  = "1h"
	actionNote = "action-link"
)

type Inbox interface {
	List(ctx context.Context, limit int) ([]*decisions.Decision, error)
	Act(ctx context.Context, id, action string, raw json.RawMessage) (*decisions.Outcome, error)
}

type Nonces interface {
	ClaimActionNonce(ctx context.Context, nonce, decisionID, action string, expires, now time.Time) (bool, error)
	ReleaseActionNonce(ctx context.Context, nonce string) error
	ActionNonceUsed(ctx context.Context, nonce string) (bool, error)
}

type Handler struct {
	Issuer    *Issuer
	Inbox     Inbox
	Nonces    Nonces
	Options   func(ctx context.Context, questionID string) ([]string, error)
	PublicURL string
	Log       *slog.Logger
	Now       func() time.Time
}

type target struct {
	decision *decisions.Decision
	label    string
	payload  json.RawMessage
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Handler) resolve(ctx context.Context, c Claims) (*target, error) {
	ds, err := h.Inbox.List(ctx, 0)
	if err != nil {
		return nil, err
	}
	var d *decisions.Decision
	for _, x := range ds {
		if x.ID == c.Decision {
			d = x
			break
		}
	}
	if d == nil {
		return nil, model.NotFound("decision %s is not pending", c.Decision)
	}
	if !slices.Contains(d.Actions, c.Action) {
		return nil, model.Conflict("action %q is not available for %s", c.Action, d.ID)
	}
	if c.Action != decisions.ActionAnswer && c.Option != 0 {
		return nil, model.BadRequest("option is only valid for answer")
	}
	t := &target{decision: d}
	var p map[string]string
	switch c.Action {
	case decisions.ActionAnswer:
		opts, err := h.Options(ctx, strings.TrimPrefix(d.ID, "q:"))
		if err != nil {
			return nil, err
		}
		if c.Option >= len(opts) {
			return nil, model.Conflict("question has no option %d", c.Option+1)
		}
		t.label, p = "Answer: "+opts[c.Option], map[string]string{"answer": opts[c.Option]}
	case decisions.ActionApprove:
		t.label, p = "Approve plan", map[string]string{"note": actionNote}
	case decisions.ActionReject:
		t.label, p = "Reject plan", map[string]string{"reason": actionNote}
	case decisions.ActionMerge:
		t.label, p = "Mark merged", map[string]string{"note": actionNote}
	case decisions.ActionUnblock:
		t.label, p = "Unblock", map[string]string{"note": actionNote}
	case decisions.ActionSkip:
		t.label, p = "Snooze for 1 hour", map[string]string{"snooze": snoozeFor}
	default:
		return nil, model.Conflict("action %q cannot be used in a link", c.Action)
	}
	t.payload, err = json.Marshal(p)
	return t, err
}

func (h *Handler) Page() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /a/{token}", h.confirm)
	mux.HandleFunc("POST /a/{token}", h.perform)
	return mux
}

func (h *Handler) confirm(w http.ResponseWriter, r *http.Request) {
	c, ok := h.verify(w, r)
	if !ok {
		return
	}
	used, err := h.Nonces.ActionNonceUsed(r.Context(), c.Nonce)
	if err != nil {
		h.failPage(w, c, err)
		return
	}
	if used {
		h.page(w, http.StatusGone, view{Heading: "Link already used", Message: "This link was already used."})
		return
	}
	t, err := h.resolve(r.Context(), c)
	if err != nil {
		h.failPage(w, c, err)
		return
	}
	cookie, field, err := h.Issuer.CSRF(c)
	if err != nil {
		h.failPage(w, c, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookiePfx + c.Nonce, Value: cookie, Path: "/a/", Expires: c.ExpiresAt(),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	h.page(w, http.StatusOK, view{
		Heading: t.label, Title: t.decision.Title, Summary: t.decision.Summary, ID: t.decision.ID,
		Expires: c.ExpiresAt().Format(time.RFC3339), CSRF: field, Confirm: true,
	})
}

func (h *Handler) perform(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		h.page(w, http.StatusBadRequest, view{Heading: "Bad request", Message: "The form could not be read."})
		return
	}
	c, ok := h.verify(w, r)
	if !ok {
		return
	}
	var cookie string
	if ck, err := r.Cookie(cookiePfx + c.Nonce); err == nil {
		cookie = ck.Value
	}
	if !h.Issuer.CheckCSRF(c, cookie, r.PostFormValue("csrf")) {
		h.page(w, http.StatusForbidden, view{Heading: "Confirmation expired", Message: "Open the link again and confirm."})
		return
	}
	ctx := r.Context()
	claimed, err := h.Nonces.ClaimActionNonce(ctx, c.Nonce, c.Decision, c.Action, c.ExpiresAt(), h.now())
	if err != nil {
		h.failPage(w, c, err)
		return
	}
	if !claimed {
		h.page(w, http.StatusGone, view{Heading: "Link already used", Message: "This link was already used."})
		return
	}
	t, err := h.resolve(ctx, c)
	if err == nil {
		_, err = h.Inbox.Act(ctx, c.Decision, c.Action, t.payload)
	}
	if err != nil {
		if rerr := h.Nonces.ReleaseActionNonce(context.WithoutCancel(ctx), c.Nonce); rerr != nil {
			h.logger().Error("action link release failed", "decision", c.Decision, "err", rerr)
		}
		h.failPage(w, c, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookiePfx + c.Nonce, Path: "/a/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	h.logger().Info("action link used", "decision", c.Decision, "action", c.Action)
	h.page(w, http.StatusOK, view{Heading: "Done", Title: t.decision.Title, ID: t.decision.ID, Message: t.label + " done."})
}

func (h *Handler) verify(w http.ResponseWriter, r *http.Request) (Claims, bool) {
	c, err := h.Issuer.Verify(r.PathValue("token"))
	if err != nil {
		h.page(w, http.StatusNotFound, view{Heading: "Link not valid", Message: "This link is invalid or has expired."})
		return Claims{}, false
	}
	return c, true
}

func (h *Handler) failPage(w http.ResponseWriter, c Claims, err error) {
	e := model.AsError(err)
	switch e.Code {
	case "not_found":
		h.page(w, http.StatusConflict, view{Heading: "Nothing to do", Message: "This decision is no longer pending."})
	case "internal":
		h.logger().Error("action link failed", "decision", c.Decision, "action", c.Action, "err", err)
		h.page(w, http.StatusInternalServerError, view{Heading: "Error", Message: "Something went wrong. Try again from the web UI."})
	default:
		h.page(w, e.HTTPStatus(), view{Heading: "Not possible", Message: e.Message})
	}
}

func (h *Handler) logger() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

type view struct {
	Heading, Title, Summary, ID, Expires, CSRF, Message string
	Confirm                                             bool
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex"><title>Subspace: {{.Heading}}</title>
<style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:2rem auto;padding:0 1rem;line-height:1.4}
button{font-size:1.1rem;padding:.6rem 1.4rem}small{color:#666}</style></head><body>
<h1>{{.Heading}}</h1>
{{if .ID}}<p><small>{{.ID}}</small></p>{{end}}
{{if .Title}}<p><strong>{{.Title}}</strong></p>{{end}}
{{if .Summary}}<p>{{.Summary}}</p>{{end}}
{{if .Message}}<p>{{.Message}}</p>{{end}}
{{if .Confirm}}<form method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><button type="submit">Confirm</button></form>
<p><small>Link valid until {{.Expires}}. Nothing happens until you confirm.</small></p>{{end}}
</body></html>
`))

func (h *Handler) page(w http.ResponseWriter, status int, v view) {
	hd := w.Header()
	hd.Set("Content-Type", "text/html; charset=utf-8")
	hd.Set("Cache-Control", "no-store")
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("X-Frame-Options", "DENY")
	hd.Set("X-Robots-Tag", "noindex")
	hd.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(status)
	_ = pageTmpl.Execute(w, v)
}

type mintRequest struct {
	DecisionID string `json:"decision_id"`
	Action     string `json:"action"`
	Option     int    `json:"option"`
	TTL        string `json:"ttl"`
}

func (h *Handler) REST() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /action-links", h.mint)
	return mux
}

func (h *Handler) mint(w http.ResponseWriter, r *http.Request) {
	var in mintRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxForm))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		h.failJSON(w, model.BadRequest("invalid JSON: %v", err))
		return
	}
	if in.DecisionID == "" || in.Action == "" {
		h.failJSON(w, model.BadRequest("decision_id and action are required"))
		return
	}
	if in.Option < 0 {
		h.failJSON(w, model.BadRequest("option must not be negative"))
		return
	}
	ttl := DefaultTTL
	if in.TTL != "" {
		v, err := time.ParseDuration(in.TTL)
		if err != nil || v < time.Minute || v > MaxTTL {
			h.failJSON(w, model.BadRequest("ttl must be a duration between 1m and %s", MaxTTL))
			return
		}
		ttl = v
	}
	if !strings.HasPrefix(h.PublicURL, "https://") {
		h.failJSON(w, model.Conflict("action links require an https SUBSPACE_PUBLIC_URL"))
		return
	}
	if _, err := h.resolve(r.Context(), Claims{Decision: in.DecisionID, Action: in.Action, Option: in.Option}); err != nil {
		h.failJSON(w, err)
		return
	}
	u, exp, err := h.URL(in.DecisionID, in.Action, in.Option, ttl)
	if err != nil {
		h.failJSON(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"url": u, "expires_at": exp})
}

func (h *Handler) URL(decision, action string, option int, ttl time.Duration) (string, time.Time, error) {
	tok, exp, err := h.Issuer.Issue(decision, action, option, ttl)
	if err != nil {
		if errors.Is(err, ErrInvalid) {
			return "", time.Time{}, model.BadRequest("invalid action link request")
		}
		return "", time.Time{}, err
	}
	return strings.TrimRight(h.PublicURL, "/") + "/a/" + tok, exp, nil
}

func (h *Handler) failJSON(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	if e.Code == "internal" {
		h.logger().Error("action link mint failed", "err", err)
	}
	writeJSON(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

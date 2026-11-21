// Package webhook receives GitHub and Forgejo/Gitea pull_request webhooks,
// verifies their HMAC signature and hands normalized events to a callback.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// MaxBodyBytes is the largest accepted request body.
const MaxBodyBytes = 1 << 20

// Provider names reported in Event.Provider.
const (
	ProviderGitHub  = "github"
	ProviderForgejo = "forgejo"
)

// Event is a normalized pull request event.
type Event struct {
	Provider   string
	Repo       string // owner/name
	Number     int
	HeadBranch string
	Merged     bool
	Closed     bool
	URL        string
}

// Func is called for every accepted pull request event.
type Func func(ctx context.Context, ev Event) error

const (
	replayTTL = 24 * time.Hour
	replayMax = 10000
)

type handler struct {
	only   string
	secret []byte
	fn     Func
	seen   *deliveries
}

type delivery struct {
	id string
	at time.Time
}

type deliveries struct {
	mu    sync.Mutex
	now   func() time.Time
	ids   map[string]time.Time
	order []delivery
}

func (d *deliveries) claim(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	for len(d.order) > 0 && (now.Sub(d.order[0].at) > replayTTL || len(d.order) >= replayMax) {
		if at, ok := d.ids[d.order[0].id]; ok && at.Equal(d.order[0].at) {
			delete(d.ids, d.order[0].id)
		}
		d.order = d.order[1:]
	}
	if _, dup := d.ids[id]; dup {
		return false
	}
	d.ids[id] = now
	d.order = append(d.order, delivery{id, now})
	return true
}

func (d *deliveries) release(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.ids, id)
}

// NewProviderHandler is like NewHandler but rejects deliveries from any other provider.
func NewProviderHandler(provider, secret string, fn Func) (http.Handler, error) {
	h, err := NewHandler(secret, fn)
	if err != nil {
		return nil, err
	}
	h.(*handler).only = provider
	return h, nil
}

// NewHandler returns a handler that verifies signatures with secret and calls fn.
func NewHandler(secret string, fn Func) (http.Handler, error) {
	if secret == "" {
		return nil, errors.New("webhook: empty secret")
	}
	if fn == nil {
		return nil, errors.New("webhook: nil callback")
	}
	return &handler{secret: []byte(secret), fn: fn, seen: &deliveries{now: time.Now, ids: map[string]time.Time{}}}, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	provider, sig, eventType, deliveryID := identify(r.Header)
	if provider == "" || (h.only != "" && provider != h.only) || !h.verify(sig, body) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	if eventType != "pull_request" && eventType != "pull_request_sync" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if deliveryID == "" {
		http.Error(w, "missing delivery id", http.StatusBadRequest)
		return
	}

	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	ev, ok := p.event(provider)
	if !ok {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if !h.seen.claim(provider + ":" + deliveryID) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if err := h.fn(r.Context(), ev); err != nil {
		h.seen.release(provider + ":" + deliveryID)
		slog.Error("webhook callback failed", "provider", ev.Provider, "repo", ev.Repo, "number", ev.Number, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// identify returns the provider, the raw signature header value, the event type and the delivery id.
func identify(h http.Header) (provider, sig, eventType, delivery string) {
	for _, name := range []string{"Forgejo", "Gitea"} {
		if s := h.Get("X-" + name + "-Signature"); s != "" {
			return ProviderForgejo, s, h.Get("X-" + name + "-Event"), h.Get("X-" + name + "-Delivery")
		}
	}
	if s := h.Get("X-Hub-Signature-256"); s != "" {
		return ProviderGitHub, s, h.Get("X-GitHub-Event"), h.Get("X-GitHub-Delivery")
	}
	return "", "", "", ""
}

func (h *handler) verify(sig string, body []byte) bool {
	sig = strings.TrimPrefix(sig, "sha256=")
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, h.secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

type payload struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Merged  bool   `json:"merged"`
		Head    struct {
			Ref string `json:"ref"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// event maps the payload to an Event; ok is false for actions we ignore.
func (p *payload) event(provider string) (Event, bool) {
	merged, closed := false, false
	switch p.Action {
	case "opened", "synchronize", "synchronized":
	case "closed":
		merged, closed = p.PullRequest.Merged, !p.PullRequest.Merged
	default:
		return Event{}, false
	}
	n := p.Number
	if n == 0 {
		n = p.PullRequest.Number
	}
	if n == 0 || p.Repository.FullName == "" {
		return Event{}, false
	}
	return Event{
		Provider:   provider,
		Repo:       p.Repository.FullName,
		Number:     n,
		HeadBranch: p.PullRequest.Head.Ref,
		Merged:     merged,
		Closed:     closed,
		URL:        p.PullRequest.HTMLURL,
	}, true
}

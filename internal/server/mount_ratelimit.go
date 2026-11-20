package server

import (
	"encoding/json"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/ratelimit"
)

const (
	envRateMCP      = "SUBSPACE_RATE_MCP"
	envRateAuthFail = "SUBSPACE_RATE_AUTHFAIL"
	defaultRateMCP  = "20/s:100"
	defaultRateFail = "5/s:50"
	lockoutsMax     = 4096
)

func init() {
	wraps = append(wraps, func(next http.Handler, d Deps) http.Handler {
		return newRateLimited(next, d, nil)
	})
}

func parseRate(v string) (ratelimit.Limit, error) {
	v = strings.TrimSpace(v)
	if v == "" || v == "0" || v == "off" {
		return ratelimit.Limit{}, nil
	}
	rate, burst, hasBurst := strings.Cut(v, ":")
	n, unit, ok := strings.Cut(rate, "/")
	if !ok {
		return ratelimit.Limit{}, strconv.ErrSyntax
	}
	per, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
	if err != nil || per <= 0 || math.IsInf(per, 0) || math.IsNaN(per) {
		return ratelimit.Limit{}, strconv.ErrSyntax
	}
	switch strings.TrimSpace(unit) {
	case "s":
		per *= 60
	case "m":
	default:
		return ratelimit.Limit{}, strconv.ErrSyntax
	}
	b := int(math.Ceil(per / 60))
	if hasBurst {
		if b, err = strconv.Atoi(strings.TrimSpace(burst)); err != nil || b < 1 {
			return ratelimit.Limit{}, strconv.ErrSyntax
		}
	}
	return ratelimit.Limit{PerMinute: per, Burst: max(b, 1)}, nil
}

func envRate(log *slog.Logger, key, def string) ratelimit.Limit {
	v := os.Getenv(key)
	if v == "" {
		v = def
	}
	lim, err := parseRate(v)
	if err != nil {
		log.Warn("invalid rate limit, using default", "env", key, "default", def)
		lim, _ = parseRate(def)
	}
	return lim
}

type rateLimited struct {
	next    http.Handler
	deps    Deps
	clock   ratelimit.Clock
	mcp     *ratelimit.Limiter
	mcpLim  ratelimit.Limit
	fails   *ratelimit.Limiter
	mu      sync.Mutex
	lockout map[string]time.Time
}

func newRateLimited(next http.Handler, d Deps, clock ratelimit.Clock) http.Handler {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	mcpLim := envRate(log, envRateMCP, defaultRateMCP)
	failLim := envRate(log, envRateAuthFail, defaultRateFail)
	if mcpLim.PerMinute <= 0 && failLim.PerMinute <= 0 {
		return next
	}
	return &rateLimited{
		next:    next,
		deps:    d,
		clock:   clock,
		mcp:     ratelimit.New(ratelimit.Options{Default: mcpLim, Clock: clock}),
		mcpLim:  mcpLim,
		fails:   ratelimit.New(ratelimit.Options{Default: failLim, Clock: clock}),
		lockout: make(map[string]time.Time),
	}
}

func (h *rateLimited) now() time.Time {
	if h.clock != nil {
		return h.clock.Now()
	}
	return time.Now()
}

func (h *rateLimited) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	isMCP := p == "/mcp"
	if !isMCP && p != "/api/v1" && !strings.HasPrefix(p, "/api/v1/") {
		h.next.ServeHTTP(w, r)
		return
	}
	ip := clientIP(r)
	if wait := h.locked(ip); wait > 0 {
		deny(w, wait)
		return
	}
	if isMCP && h.mcpLim.PerMinute > 0 {
		if id := h.agentID(r); id != "" && !h.mcp.Allow(id) {
			deny(w, time.Duration(60/h.mcpLim.PerMinute*float64(time.Second)))
			return
		}
	}
	h.next.ServeHTTP(&failWatcher{ResponseWriter: w, onFail: func() { h.fail(ip) }}, r)
}

// placeholder
func (h *rateLimited) agentID(r *http.Request) string {
	const prefix = "Bearer "
	hd := r.Header.Get("Authorization")
	if h.deps.Auth == nil || len(hd) <= len(prefix) || hd[:len(prefix)] != prefix {
		return ""
	}
	a, err := h.deps.Auth.LookupToken(hd[len(prefix):])
	if err != nil || a == nil || a.Role == model.RoleLeader || a.Role == model.RoleReviewer {
		return ""
	}
	return a.ID
}

func (h *rateLimited) locked(ip string) time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	until, ok := h.lockout[ip]
	if !ok {
		return 0
	}
	wait := until.Sub(h.now())
	if wait <= 0 {
		delete(h.lockout, ip)
		return 0
	}
	return wait
}

func (h *rateLimited) fail(ip string) {
	wait := h.fails.Reserve(ip)
	if wait <= 0 {
		return
	}
	now := h.now()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.lockout) >= lockoutsMax {
		for k, until := range h.lockout {
			if !until.After(now) {
				delete(h.lockout, k)
			}
		}
	}
	h.lockout[ip] = now.Add(wait)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func deny(w http.ResponseWriter, wait time.Duration) {
	secs := max(int(math.Ceil(wait.Seconds())), 1)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "rate_limited", "message": "too many requests"}})
}

type failWatcher struct {
	http.ResponseWriter
	onFail func()
}

func (f *failWatcher) WriteHeader(code int) {
	if code == http.StatusUnauthorized {
		f.onFail()
	}
	f.ResponseWriter.WriteHeader(code)
}

func (f *failWatcher) Flush() {
	_ = http.NewResponseController(f.ResponseWriter).Flush()
}

func (f *failWatcher) Unwrap() http.ResponseWriter { return f.ResponseWriter }

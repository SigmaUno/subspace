package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"

	"github.com/SigmaUno/subspace/internal/httpx"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/reload"
)

const (
	envOwnerAllowCIDRs = "SUBSPACE_OWNER_ALLOW_CIDRS"
	envAgentAllowCIDRs = "SUBSPACE_AGENT_ALLOW_CIDRS"
	envTrustedProxies  = "SUBSPACE_TRUSTED_PROXIES"
)

type allowRule struct {
	open bool
	nets []netip.Prefix
}

func (r allowRule) allows(a netip.Addr) bool {
	return r.open || inPrefixes(r.nets, a)
}

type allowState struct {
	owner allowRule
	agent allowRule
}

type proxyTrust struct {
	nets atomic.Pointer[[]netip.Prefix]
}

func newProxyTrust(log *slog.Logger, getenv func(string) string) *proxyTrust {
	t := &proxyTrust{}
	nets, err := trustedFrom(getenv)
	if err != nil {
		log.Error("invalid trusted proxies, ignoring X-Forwarded-For", "env", envTrustedProxies, "err", err)
	}
	t.nets.Store(&nets)
	return t
}

func trustedFrom(getenv func(string) string) ([]netip.Prefix, error) {
	v := strings.TrimSpace(getenv(envTrustedProxies))
	if v == "" {
		return nil, nil
	}
	nets, err := parsePrefixes(v)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", envTrustedProxies, err)
	}
	return nets, nil
}

func (t *proxyTrust) get() []netip.Prefix { return *t.nets.Load() }

func (t *proxyTrust) PrepareReload(getenv func(string) string) (reload.Plan, error) {
	nets, err := trustedFrom(getenv)
	if err != nil {
		return reload.Plan{}, err
	}
	return reload.Plan{Commit: func() { t.nets.Store(&nets) }}, nil
}

type allowlisted struct {
	next  http.Handler
	state atomic.Pointer[allowState]
	trust *proxyTrust
}

func newAllowlisted(next http.Handler, d Deps) http.Handler {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	h := &allowlisted{next: next, trust: d.Trust}
	if h.trust == nil {
		h.trust = newProxyTrust(log, d.getenv())
	}
	h.state.Store(&allowState{
		owner: envRule(log, d.getenv(), envOwnerAllowCIDRs),
		agent: envRule(log, d.getenv(), envAgentAllowCIDRs),
	})
	d.reloadable("allowlist", []string{envOwnerAllowCIDRs, envAgentAllowCIDRs}, h)
	return h
}

func (h *allowlisted) PrepareReload(getenv func(string) string) (reload.Plan, error) {
	owner, err := ruleFrom(getenv, envOwnerAllowCIDRs)
	agent, aerr := ruleFrom(getenv, envAgentAllowCIDRs)
	if err = errors.Join(err, aerr); err != nil {
		return reload.Plan{}, err
	}
	st := &allowState{owner: owner, agent: agent}
	return reload.Plan{Commit: func() { h.state.Store(st) }}, nil
}

func ruleFrom(getenv func(string) string, key string) (allowRule, error) {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return allowRule{open: true}, nil
	}
	nets, err := parsePrefixes(v)
	if err != nil {
		return allowRule{}, fmt.Errorf("%s: %w", key, err)
	}
	return allowRule{nets: nets}, nil
}

func envRule(log *slog.Logger, getenv func(string) string, key string) allowRule {
	r, err := ruleFrom(getenv, key)
	if err != nil {
		log.Error("invalid allowlist, denying all requests to this surface", "env", key, "err", err)
		return allowRule{}
	}
	return r
}

func parsePrefixes(v string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			a, aerr := netip.ParseAddr(s)
			if aerr != nil {
				return nil, errors.New("invalid CIDR or address: " + s)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, errors.New("no entries")
	}
	return out, nil
}

func (h *allowlisted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rule, guarded := h.rule(h.state.Load(), r.URL.Path)
	if !guarded {
		h.next.ServeHTTP(w, r)
		return
	}
	ip, ok := h.client(r)
	if !ok || !rule.allows(ip) {
		forbid(w)
		return
	}
	h.next.ServeHTTP(w, r)
}

func (h *allowlisted) rule(st *allowState, p string) (allowRule, bool) {
	switch {
	case p == "/mcp":
		return st.agent, true
	case p == "/api/v1" || strings.HasPrefix(p, "/api/v1/"):
		return st.owner, true
	}
	return allowRule{}, false
}

func (h *allowlisted) client(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	trusted := h.trust.get()
	peer, ok := parseHop(host)
	if !ok || !inPrefixes(trusted, peer) {
		return peer, ok
	}
	fwd := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	if strings.TrimSpace(fwd) == "" {
		return peer, true
	}
	hops := strings.Split(fwd, ",")
	for i := len(hops) - 1; i > 0; i-- {
		a, ok := parseHop(hops[i])
		if !ok {
			return netip.Addr{}, false
		}
		if !inPrefixes(trusted, a) {
			return a, true
		}
	}
	return parseHop(hops[0])
}

func parseHop(s string) (netip.Addr, bool) {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

func inPrefixes(nets []netip.Prefix, a netip.Addr) bool {
	for _, p := range nets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func forbid(w http.ResponseWriter) {
	httpx.WriteError(w, model.Forbidden("request not allowed from this address"))
}

package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
)

const (
	envOwnerAllowCIDRs = "SUBSPACE_OWNER_ALLOW_CIDRS"
	envAgentAllowCIDRs = "SUBSPACE_AGENT_ALLOW_CIDRS"
	envTrustedProxies  = "SUBSPACE_TRUSTED_PROXIES"
)

func init() {
	wraps = append(wraps, func(next http.Handler, d Deps) http.Handler {
		return newAllowlisted(next, d)
	})
}

type allowRule struct {
	open bool
	nets []netip.Prefix
}

func (r allowRule) allows(a netip.Addr) bool {
	return r.open || inPrefixes(r.nets, a)
}

type allowlisted struct {
	next    http.Handler
	owner   allowRule
	agent   allowRule
	trusted []netip.Prefix
}

func newAllowlisted(next http.Handler, d Deps) http.Handler {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	h := &allowlisted{
		next:  next,
		owner: envRule(log, envOwnerAllowCIDRs),
		agent: envRule(log, envAgentAllowCIDRs),
	}
	if v := strings.TrimSpace(os.Getenv(envTrustedProxies)); v != "" {
		nets, err := parsePrefixes(v)
		if err != nil {
			log.Error("invalid trusted proxies, ignoring X-Forwarded-For", "env", envTrustedProxies, "err", err)
		}
		h.trusted = nets
	}
	return h
}

func envRule(log *slog.Logger, key string) allowRule {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return allowRule{open: true}
	}
	nets, err := parsePrefixes(v)
	if err != nil {
		log.Error("invalid allowlist, denying all requests to this surface", "env", key, "err", err)
		return allowRule{}
	}
	return allowRule{nets: nets}
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
	rule, guarded := h.rule(r.URL.Path)
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

func (h *allowlisted) rule(p string) (allowRule, bool) {
	switch {
	case p == "/mcp":
		return h.agent, true
	case p == "/api/v1" || strings.HasPrefix(p, "/api/v1/"):
		return h.owner, true
	}
	return allowRule{}, false
}

func (h *allowlisted) client(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, ok := parseHop(host)
	if !ok || !inPrefixes(h.trusted, peer) {
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
		if !inPrefixes(h.trusted, a) {
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "forbidden", "message": "request not allowed from this address"}})
}

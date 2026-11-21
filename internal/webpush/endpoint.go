package webpush

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

const maxEndpoint = 2048

var DefaultHosts = []string{
	"fcm.googleapis.com",
	"android.googleapis.com",
	"*.push.services.mozilla.com",
	"*.push.apple.com",
	"*.notify.windows.com",
}

type Hosts struct {
	patterns []string
}

func NewHosts(extra string) (*Hosts, error) {
	h := &Hosts{patterns: append([]string(nil), DefaultHosts...)}
	for _, p := range strings.Split(extra, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		name := strings.TrimPrefix(p, "*.")
		if host, port, err := net.SplitHostPort(name); err == nil {
			if strings.HasPrefix(p, "*.") || port == "" {
				return nil, fmt.Errorf("invalid push host %q", p)
			}
			name = host
		}
		if name == "" || strings.ContainsAny(name, "*/@?# ") {
			return nil, fmt.Errorf("invalid push host %q", p)
		}
		h.patterns = append(h.patterns, p)
	}
	return h, nil
}

func (h *Hosts) match(host, port string) bool {
	full := host
	if port != "" {
		full = net.JoinHostPort(host, port)
	}
	for _, p := range h.patterns {
		switch {
		case p == full:
			return true
		case port != "" && port != "443":
		case p == host:
			return true
		case strings.HasPrefix(p, "*.") && strings.HasSuffix(host, p[1:]) && len(host) > len(p)-1:
			return true
		}
	}
	return false
}

func (h *Hosts) Check(raw string, blocked func(netip.Addr) bool) (*url.URL, error) {
	if raw == "" || len(raw) > maxEndpoint {
		return nil, fmt.Errorf("endpoint must be 1 to %d bytes", maxEndpoint)
	}
	if strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r == 0x7f || r == '\\' }) {
		return nil, errors.New("endpoint contains invalid characters")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Opaque != "" {
		return nil, errors.New("endpoint is not a valid URL")
	}
	if u.Scheme != "https" {
		return nil, errors.New("endpoint must use https")
	}
	if u.User != nil || u.Fragment != "" || u.RawFragment != "" {
		return nil, errors.New("endpoint must not carry credentials or a fragment")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || strings.HasSuffix(host, ".") {
		return nil, errors.New("endpoint needs a host name")
	}
	if ip, err := netip.ParseAddr(host); err == nil && blocked(ip) {
		return nil, errors.New("endpoint address is not allowed")
	}
	if !h.match(host, u.Port()) {
		return nil, fmt.Errorf("endpoint host %s is not a known push service", host)
	}
	u.Host = strings.ToLower(u.Host)
	return u, nil
}

func origin(u *url.URL) string {
	host := u.Host
	if u.Port() == "443" {
		host = u.Hostname()
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	}
	return u.Scheme + "://" + host
}

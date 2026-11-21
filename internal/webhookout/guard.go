package webhookout

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var ErrBlocked = errors.New("destination address is not allowed")

var blockedPrefixes = mustPrefixes(
	"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
	"100::/64", "2001:db8::/32", "fec0::/10",
)

var (
	nat64     = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour = netip.MustParsePrefix("2002::/16")
)

var metadataAddrs = []netip.Addr{netip.MustParseAddr("169.254.169.254"), netip.MustParseAddr("fd00:ec2::254")}

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

func ParsePrefixes(list string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, e := range strings.Split(list, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if p, err := netip.ParsePrefix(e); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(e)
		if err != nil {
			return nil, fmt.Errorf("invalid address or CIDR %q", e)
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type Guard struct {
	Resolver Resolver
	Dial     func(ctx context.Context, network, addr string) (net.Conn, error)
	Allow    []netip.Prefix
}

func (g *Guard) Blocked(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	for _, m := range metadataAddrs {
		if ip == m {
			return true
		}
	}
	for _, p := range g.Allow {
		if p.Contains(ip) {
			return false
		}
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || !ip.IsValid() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	if v4, ok := embeddedV4(ip); ok {
		return g.Blocked(v4)
	}
	return false
}

func embeddedV4(ip netip.Addr) (netip.Addr, bool) {
	if !ip.Is6() {
		return netip.Addr{}, false
	}
	b := ip.As16()
	switch {
	case nat64.Contains(ip):
		return netip.AddrFrom4([4]byte(b[12:16])), true
	case sixToFour.Contains(ip):
		return netip.AddrFrom4([4]byte(b[2:6])), true
	}
	return netip.Addr{}, false
}

func (g *Guard) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	ips, err := g.Resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses for %s", host)
	}
	return ips, nil
}

func (g *Guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := g.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if g.Blocked(ip) {
			return nil, ErrBlocked
		}
	}
	var last error
	for _, ip := range ips {
		c, err := g.Dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

func (g *Guard) CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 {
		return nil, errors.New("url is not valid")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("url scheme must be http or https")
	}
	if u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("url needs a host and no credentials or fragment")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && g.Blocked(ip) {
		return nil, ErrBlocked
	}
	return u, nil
}

func NewGuard(allow []netip.Prefix) *Guard {
	d := &net.Dialer{Timeout: 10 * time.Second}
	return &Guard{Resolver: net.DefaultResolver, Dial: d.DialContext, Allow: allow}
}

func (g *Guard) Client(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         g.DialContext,
			DisableKeepAlives:   true,
			TLSHandshakeTimeout: 10 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

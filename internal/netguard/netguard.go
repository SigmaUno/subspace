package netguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var ErrBlocked = errors.New("destination address is not allowed")

const (
	MaxURLLen          = 2048
	defaultDialTimeout = 10 * time.Second
)

var reserved = mustPrefixes(
	"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15",
	"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
	"::/96", "64:ff9b:1::/48", "100::/64", "2001::/32", "2001:db8::/32", "fec0::/10",
)

var (
	nat64     = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour = netip.MustParsePrefix("2002::/16")
)

var metadata = []netip.Addr{netip.MustParseAddr("169.254.169.254"), netip.MustParseAddr("fd00:ec2::254")}

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

func Blocked(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, p := range reserved {
		if p.Contains(ip) {
			return true
		}
	}
	if v4, ok := embeddedV4(ip); ok {
		return Blocked(v4)
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

func LocalName(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type Guard struct {
	Resolver      Resolver
	Dial          func(ctx context.Context, network, addr string) (net.Conn, error)
	Allow         []netip.Prefix
	AllowAll      bool
	Err           error
	DialTimeout   time.Duration
	LookupTimeout time.Duration
}

func New(allow []netip.Prefix) *Guard {
	return &Guard{Allow: allow}
}

func (g *Guard) blockedErr() error {
	if g.Err != nil {
		return g.Err
	}
	return ErrBlocked
}

func (g *Guard) Blocked(ip netip.Addr) bool {
	if g.AllowAll {
		return false
	}
	ip = ip.Unmap().WithZone("")
	for _, m := range metadata {
		if ip == m {
			return true
		}
	}
	for _, p := range g.Allow {
		if p.Contains(ip) {
			return false
		}
	}
	return Blocked(ip)
}

func (g *Guard) CheckHost(host string) error {
	if host == "" {
		return errors.New("url has no host")
	}
	if g.AllowAll {
		return nil
	}
	if LocalName(host) {
		return g.blockedErr()
	}
	if ip, err := netip.ParseAddr(host); err == nil && g.Blocked(ip) {
		return g.blockedErr()
	}
	return nil
}

func (g *Guard) CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > MaxURLLen {
		return nil, errors.New("url is not valid")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("url scheme must be http or https")
	}
	if u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("url needs a host and no credentials or fragment")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && g.Blocked(ip) {
		return nil, g.blockedErr()
	}
	return u, nil
}

func (g *Guard) Resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else {
		r := g.Resolver
		if r == nil {
			r = net.DefaultResolver
		}
		if g.LookupTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, g.LookupTimeout)
			defer cancel()
		}
		ips, err = r.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses for %s", host)
	}
	for _, ip := range ips {
		if g.Blocked(ip) {
			return nil, g.blockedErr()
		}
	}
	return ips, nil
}

func (g *Guard) dial() func(ctx context.Context, network, addr string) (net.Conn, error) {
	if g.Dial != nil {
		return g.Dial
	}
	t := g.DialTimeout
	if t <= 0 {
		t = defaultDialTimeout
	}
	d := &net.Dialer{Timeout: t}
	return d.DialContext
}

func (g *Guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := g.Resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	dial := g.dial()
	var errs error
	for _, ip := range ips {
		c, err := dial(ctx, network, net.JoinHostPort(ip.Unmap().WithZone("").String(), port))
		if err == nil {
			return c, nil
		}
		errs = errors.Join(errs, err)
	}
	return nil, errs
}

func NoRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func (g *Guard) Transport() *http.Transport {
	t := g.DialTimeout
	if t <= 0 {
		t = defaultDialTimeout
	}
	return &http.Transport{
		Proxy:               nil,
		DialContext:         g.DialContext,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: t,
	}
}

func (g *Guard) Client(timeout time.Duration) *http.Client {
	t := g.Transport()
	t.ResponseHeaderTimeout = timeout
	return &http.Client{Timeout: timeout, Transport: t, CheckRedirect: NoRedirect}
}

func ReadBody(r io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, limit))
}

func Drain(r io.Reader, limit int64) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r, limit))
}

func Scrub(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

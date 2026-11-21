package callback

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

const (
	MaxURLLen   = 2048
	dialTimeout = 5 * time.Second
	lookupLimit = 5 * time.Second
)

var ErrBlocked = errors.New("callback: target address is not allowed")

var reserved = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "::/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64",
		"2001::/32", "2001:db8::/32", "2002::/16", "fec0::/10",
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

func blocked(a netip.Addr) bool {
	a = a.Unmap().WithZone("")
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return true
	}
	for _, p := range reserved {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type Policy struct {
	AllowPrivate bool
	Resolver     Resolver
}

func (p Policy) resolver() Resolver {
	if p.Resolver != nil {
		return p.Resolver
	}
	return net.DefaultResolver
}

func (p Policy) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		ctx, cancel := context.WithTimeout(ctx, lookupLimit)
		defer cancel()
		addrs, err = p.resolver().LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("callback: resolve %s: %w", host, err)
		}
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("callback: %s has no addresses", host)
	}
	if !p.AllowPrivate {
		for _, a := range addrs {
			if blocked(a) {
				return nil, ErrBlocked
			}
		}
	}
	return addrs, nil
}

func (p Policy) Validate(ctx context.Context, raw string) error {
	if len(raw) > MaxURLLen {
		return fmt.Errorf("callback: url longer than %d bytes", MaxURLLen)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("callback: invalid url")
	}
	if u.Scheme != "https" && (!p.AllowPrivate || u.Scheme != "http") {
		return errors.New("callback: url must use https")
	}
	if u.User != nil {
		return errors.New("callback: url must not contain credentials")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return errors.New("callback: url has no host")
	}
	if !p.AllowPrivate && (host == "localhost" || strings.HasSuffix(host, ".localhost")) {
		return ErrBlocked
	}
	_, err = p.resolve(ctx, host)
	return err
}

func (p Policy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	addrs, err := p.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: dialTimeout}
	var errs error
	for _, a := range addrs {
		c, err := d.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
		if err == nil {
			return c, nil
		}
		errs = errors.Join(errs, err)
	}
	return nil, errs
}

func (p Policy) NewClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           p.DialContext,
			DisableKeepAlives:     true,
			TLSHandshakeTimeout:   dialTimeout,
			ResponseHeaderTimeout: timeout,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

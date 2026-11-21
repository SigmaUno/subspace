package notify

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

var ErrBlockedTarget = errors.New("notify: target address is not allowed")

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func blockedAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified() || cgnat.Contains(a)
}

func CheckURL(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("notify: invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("notify: unsupported scheme %q", u.Scheme)
	}
	return checkHost(strings.ToLower(u.Hostname()), allowPrivate)
}

func checkHost(host string, allowPrivate bool) error {
	if host == "" {
		return errors.New("notify: url has no host")
	}
	if allowPrivate {
		return nil
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return ErrBlockedTarget
	}
	if a, err := netip.ParseAddr(host); err == nil && blockedAddr(a) {
		return ErrBlockedTarget
	}
	return nil
}

func guardedDialer(allowPrivate bool) *net.Dialer {
	d := &net.Dialer{Timeout: 5 * time.Second}
	if !allowPrivate {
		d.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			a, err := netip.ParseAddr(host)
			if err != nil || blockedAddr(a) {
				return ErrBlockedTarget
			}
			return nil
		}
	}
	return d
}

func NewClient(allowPrivate bool, timeout time.Duration) *http.Client {
	d := guardedDialer(allowPrivate)
	return &http.Client{
		Timeout:       timeout,
		Transport:     &http.Transport{DialContext: d.DialContext, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

package notify

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/netguard"
)

var ErrBlockedTarget = errors.New("notify: target address is not allowed")

func newGuard(allowPrivate bool) *netguard.Guard {
	return &netguard.Guard{AllowAll: allowPrivate, Err: ErrBlockedTarget, DialTimeout: 5 * time.Second}
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
	return newGuard(allowPrivate).CheckHost(host)
}

func NewClient(allowPrivate bool, timeout time.Duration) *http.Client {
	t := newGuard(allowPrivate).Transport()
	t.DisableKeepAlives = false
	t.MaxIdleConns = 4
	t.IdleConnTimeout = 30 * time.Second
	return &http.Client{Timeout: timeout, Transport: t, CheckRedirect: netguard.NoRedirect}
}

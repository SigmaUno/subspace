package callback

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/netguard"
)

const (
	MaxURLLen   = netguard.MaxURLLen
	dialTimeout = 5 * time.Second
	lookupLimit = 5 * time.Second
)

var ErrBlocked = errors.New("callback: target address is not allowed")

type Resolver = netguard.Resolver

type Policy struct {
	AllowPrivate bool
	Resolver     Resolver
}

func (p Policy) guard() *netguard.Guard {
	return &netguard.Guard{
		AllowAll:      p.AllowPrivate,
		Resolver:      p.Resolver,
		Err:           ErrBlocked,
		DialTimeout:   dialTimeout,
		LookupTimeout: lookupLimit,
	}
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
	g := p.guard()
	if err := g.CheckHost(host); err != nil {
		return err
	}
	if _, err := g.Resolve(ctx, host); err != nil {
		if errors.Is(err, ErrBlocked) {
			return err
		}
		return fmt.Errorf("callback: resolve %s: %w", host, err)
	}
	return nil
}

func (p Policy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return p.guard().DialContext(ctx, network, addr)
}

func (p Policy) NewClient(timeout time.Duration) *http.Client {
	return p.guard().Client(timeout)
}

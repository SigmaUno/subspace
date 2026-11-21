package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/netguard"
)

var errDenied = errors.New("denied")

var hopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

type pin struct {
	ips     []netip.Addr
	expires time.Time
}

type proxy struct {
	allow    []string
	private  map[string]bool
	ports    map[int]bool
	resolver netguard.Resolver
	dial     func(ctx context.Context, network, addr string) (net.Conn, error)
	pinTTL   time.Duration
	idle     time.Duration
	now      func() time.Time
	log      *slog.Logger
	slots    chan struct{}

	mu   sync.Mutex
	pins map[string]pin
}

type options struct {
	allow    []string
	private  []string
	ports    []int
	resolver netguard.Resolver
	dial     func(ctx context.Context, network, addr string) (net.Conn, error)
	pinTTL   time.Duration
	idle     time.Duration
	maxConns int
	now      func() time.Time
	log      *slog.Logger
}

func newProxy(o options) (*proxy, error) {
	p := &proxy{
		private: map[string]bool{}, ports: map[int]bool{}, resolver: o.resolver, dial: o.dial,
		pinTTL: o.pinTTL, idle: o.idle, now: o.now, log: o.log, pins: map[string]pin{},
	}
	for _, h := range o.allow {
		h = normHost(h)
		if _, err := netip.ParseAddr(h); err == nil || h == "" || strings.ContainsAny(h, ":/[]") {
			return nil, fmt.Errorf("allow entry %q must be a host name or *.domain", h)
		}
		p.allow = append(p.allow, h)
	}
	for _, h := range o.private {
		p.private[normHost(h)] = true
	}
	for _, port := range o.ports {
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid port %d", port)
		}
		p.ports[port] = true
	}
	if p.resolver == nil {
		p.resolver = net.DefaultResolver
	}
	if p.dial == nil {
		d := &net.Dialer{Timeout: 10 * time.Second}
		p.dial = d.DialContext
	}
	if p.now == nil {
		p.now = time.Now
	}
	if p.log == nil {
		p.log = slog.Default()
	}
	if p.pinTTL <= 0 {
		p.pinTTL = 5 * time.Minute
	}
	if p.idle <= 0 {
		p.idle = 5 * time.Minute
	}
	p.slots = make(chan struct{}, max(o.maxConns, 1))
	return p, nil
}

func normHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}

func (p *proxy) allowed(host string) bool {
	for _, a := range p.allow {
		switch {
		case strings.HasPrefix(a, "*."):
			if strings.HasSuffix(host, a[1:]) && len(host) > len(a)-1 {
				return true
			}
		case strings.HasPrefix(a, "."):
			if strings.HasSuffix(host, a) && len(host) > len(a) {
				return true
			}
		case host == a:
			return true
		}
	}
	return false
}

func (p *proxy) check(ctx context.Context, hostport string, defPort int) (string, []netip.Addr, int, error) {
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		host, portStr = hostport, strconv.Itoa(defPort)
	}
	host = normHost(host)
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return host, nil, 0, fmt.Errorf("%w: bad port", errDenied)
	}
	if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return host, nil, port, fmt.Errorf("%w: ip literal", errDenied)
	}
	if !p.allowed(host) {
		return host, nil, port, fmt.Errorf("%w: host not allowed", errDenied)
	}
	if !p.ports[port] {
		return host, nil, port, fmt.Errorf("%w: port not allowed", errDenied)
	}
	ips, err := p.resolve(ctx, host)
	return host, ips, port, err
}

func (p *proxy) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	now := p.now()
	p.mu.Lock()
	if pn, ok := p.pins[host]; ok && now.Before(pn.expires) {
		p.mu.Unlock()
		return pn.ips, nil
	}
	p.mu.Unlock()
	g := &netguard.Guard{Resolver: p.resolver, LookupTimeout: 5 * time.Second, Err: fmt.Errorf("%w: address not allowed", errDenied)}
	if p.private[host] {
		g.Allow = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")}
	}
	ips, err := g.Resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if ip.IsUnspecified() || ip.IsMulticast() {
			return nil, fmt.Errorf("%w: address not allowed", errDenied)
		}
	}
	p.mu.Lock()
	p.pins[host] = pin{ips: ips, expires: now.Add(p.pinTTL)}
	p.mu.Unlock()
	return ips, nil
}

func (p *proxy) dialPinned(ctx context.Context, ips []netip.Addr, port int) (net.Conn, error) {
	var errs error
	for _, ip := range ips {
		c, err := p.dial(ctx, "tcp", net.JoinHostPort(ip.Unmap().WithZone("").String(), strconv.Itoa(port)))
		if err == nil {
			return c, nil
		}
		errs = errors.Join(errs, err)
	}
	return nil, errs
}

type entry struct {
	client string
	method string
	host   string
	port   int
	start  time.Time
}

func (p *proxy) record(e entry, status int, err error, up, down int64) {
	attrs := []any{
		"client", e.client, "method", e.method, "host", e.host, "port", e.port, "status", status,
		"bytes_up", up, "bytes_down", down, "ms", p.now().Sub(e.start).Milliseconds(),
	}
	if err != nil {
		p.log.Warn("egress denied", append(attrs, "reason", err.Error())...)
		return
	}
	p.log.Info("egress", attrs...)
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e := entry{client: r.RemoteAddr, method: r.Method, start: p.now()}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		p.record(e, http.StatusServiceUnavailable, errors.New("connection limit"), 0, 0)
		return
	}
	if r.Method == http.MethodConnect {
		p.connect(w, r, e)
		return
	}
	p.forward(w, r, e)
}

func (p *proxy) deny(w http.ResponseWriter, e entry, err error) {
	status := http.StatusBadGateway
	if errors.Is(err, errDenied) {
		status = http.StatusForbidden
	}
	http.Error(w, http.StatusText(status), status)
	p.record(e, status, err, 0, 0)
}

func (p *proxy) connect(w http.ResponseWriter, r *http.Request, e entry) {
	host, ips, port, err := p.check(r.Context(), r.Host, 443)
	e.host, e.port = host, port
	if err != nil {
		p.deny(w, e, err)
		return
	}
	up, err := p.dialPinned(r.Context(), ips, port)
	if err != nil {
		p.deny(w, e, err)
		return
	}
	defer func() { _ = up.Close() }()
	hj, ok := w.(http.Hijacker)
	if !ok {
		p.deny(w, e, errors.New("hijack unsupported"))
		return
	}
	client, brw, err := hj.Hijack()
	if err != nil {
		p.record(e, 0, err, 0, 0)
		return
	}
	defer func() { _ = client.Close() }()
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	var sent int64
	if n := brw.Reader.Buffered(); n > 0 {
		b, _ := brw.Peek(n)
		k, err := up.Write(b)
		sent += int64(k)
		if err != nil {
			return
		}
	}
	var wg sync.WaitGroup
	var down int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		down = p.pipe(client, up)
	}()
	sent += p.pipe(up, client)
	wg.Wait()
	p.record(e, http.StatusOK, nil, sent, down)
}

func (p *proxy) pipe(dst, src net.Conn) int64 {
	buf := make([]byte, 32<<10)
	var total int64
	for {
		_ = src.SetReadDeadline(time.Now().Add(p.idle))
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				break
			}
			total += int64(n)
		}
		if err != nil {
			break
		}
	}
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	} else {
		_ = dst.SetReadDeadline(time.Now())
	}
	return total
}

func (p *proxy) forward(w http.ResponseWriter, r *http.Request, e entry) {
	if !r.URL.IsAbs() || r.URL.Scheme != "http" || r.URL.User != nil {
		p.deny(w, e, fmt.Errorf("%w: only absolute http urls and CONNECT are proxied", errDenied))
		return
	}
	host, ips, port, err := p.check(r.Context(), r.URL.Host, 80)
	e.host, e.port = host, port
	if err != nil {
		p.deny(w, e, err)
		return
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: time.Minute,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return p.dialPinned(ctx, ips, port)
		},
	}
	defer tr.CloseIdleConnections()
	out := r.Clone(r.Context())
	out.RequestURI = ""
	stripHop(out.Header)
	resp, err := tr.RoundTrip(out)
	if err != nil {
		p.deny(w, e, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	stripHop(resp.Header)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	n, _ := io.Copy(w, resp.Body)
	p.record(e, resp.StatusCode, nil, max(r.ContentLength, 0), n)
}

func stripHop(h http.Header) {
	for _, f := range strings.Split(h.Get("Connection"), ",") {
		if f = strings.TrimSpace(f); f != "" {
			h.Del(f)
		}
	}
	for _, k := range hopHeaders {
		h.Del(k)
	}
}

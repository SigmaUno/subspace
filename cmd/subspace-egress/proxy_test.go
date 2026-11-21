package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeDNS struct {
	mu      sync.Mutex
	records map[string][]netip.Addr
	lookups map[string]int
}

func (f *fakeDNS) set(host string, ips ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var addrs []netip.Addr
	for _, ip := range ips {
		addrs = append(addrs, netip.MustParseAddr(ip))
	}
	f.records[host] = addrs
}

func (f *fakeDNS) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups[host]++
	if ips, ok := f.records[host]; ok {
		return ips, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

type rig struct {
	dns    *fakeDNS
	proxy  *proxy
	addr   string
	logs   *syncBuffer
	now    atomic.Int64
	mu     sync.Mutex
	dialed []string
	routes map[string]string
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) entries(t *testing.T) []map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(s.b.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad log line %q", line)
		}
		out = append(out, m)
	}
	return out
}

func newRig(t *testing.T, upstream string) *rig {
	t.Helper()
	r := &rig{
		dns:    &fakeDNS{records: map[string][]netip.Addr{}, lookups: map[string]int{}},
		logs:   &syncBuffer{},
		routes: map[string]string{},
	}
	r.now.Store(time.Unix(1_700_000_000, 0).UnixNano())
	p, err := newProxy(options{
		allow:    []string{"forge.test", "*.cdn.test", "subspace.test", "pmeta.test"},
		private:  []string{"subspace.test", "pmeta.test"},
		ports:    []int{443, 80},
		resolver: r.dns,
		dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			r.mu.Lock()
			r.dialed = append(r.dialed, addr)
			target := upstream
			if v, ok := r.routes[addr]; ok {
				target = v
			}
			r.mu.Unlock()
			var d net.Dialer
			return d.DialContext(ctx, network, target)
		},
		pinTTL:   time.Minute,
		maxConns: 16,
		now:      func() time.Time { return time.Unix(0, r.now.Load()) },
		log:      slog.New(slog.NewJSONHandler(r.logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	r.proxy = p
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	r.addr = srv.Listener.Addr().String()
	return r
}

func (r *rig) dials() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.dialed)
}

func (r *rig) connect(t *testing.T, target string) (int, string) {
	t.Helper()
	c, err := net.Dial("tcp", r.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("%s: %v", target, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return resp.StatusCode, ""
	}
	_, _ = fmt.Fprintf(c, "GET /tunnel HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target)
	inner, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("%s: tunnel: %v", target, err)
	}
	defer func() { _ = inner.Body.Close() }()
	body, _ := io.ReadAll(inner.Body)
	return resp.StatusCode, string(body)
}

func upstreamServer(t *testing.T, body string, hits *atomic.Int32, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if h != nil {
			h(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

func TestConnectAllowlist(t *testing.T) {
	r := newRig(t, upstreamServer(t, "upstream", nil, nil))
	r.dns.set("forge.test", "93.184.216.34")
	r.dns.set("x.cdn.test", "93.184.216.35")
	r.dns.set("cdn.test", "93.184.216.36")
	r.dns.set("forge.test.evil.com", "93.184.216.37")
	r.dns.set("evilforge.test", "93.184.216.38")
	r.dns.set("localhost", "127.0.0.1")
	r.dns.set("internal.cdn.test", "10.1.2.3")
	r.dns.set("meta.cdn.test", "169.254.169.254")
	r.dns.set("loop.cdn.test", "127.0.0.1")
	r.dns.set("mixed.cdn.test", "93.184.216.39", "192.168.1.1")
	r.dns.set("mapped.cdn.test", "::ffff:10.0.0.1")
	r.dns.set("ula.cdn.test", "fd12::1")
	r.dns.set("cgnat.cdn.test", "100.64.0.1")
	r.dns.set("zero.cdn.test", "0.0.0.0")
	r.dns.set("subspace.test", "172.18.0.5")
	r.dns.set("pmeta.test", "169.254.169.254")

	cases := []struct {
		target string
		want   int
	}{
		{"forge.test:443", 200},
		{"FORGE.TEST.:443", 200},
		{"x.cdn.test:443", 200},
		{"subspace.test:443", 200},
		{"cdn.test:443", 403},
		{"forge.test.evil.com:443", 403},
		{"evilforge.test:443", 403},
		{"forge.test:22", 403},
		{"forge.test:0", 403},
		{"localhost:443", 403},
		{"93.184.216.34:443", 403},
		{"[::1]:443", 403},
		{"[::ffff:127.0.0.1]:443", 403},
		{"2130706433:443", 403},
		{"0x7f000001:443", 403},
		{"internal.cdn.test:443", 403},
		{"meta.cdn.test:443", 403},
		{"loop.cdn.test:443", 403},
		{"mixed.cdn.test:443", 403},
		{"mapped.cdn.test:443", 403},
		{"ula.cdn.test:443", 403},
		{"cgnat.cdn.test:443", 403},
		{"zero.cdn.test:443", 403},
		{"pmeta.test:443", 403},
		{"missing.cdn.test:443", 502},
	}
	for _, c := range cases {
		status, body := r.connect(t, c.target)
		if status != c.want {
			t.Errorf("%s: status %d, want %d", c.target, status, c.want)
		}
		if status == 200 && body != "upstream" {
			t.Errorf("%s: tunnel body %q", c.target, body)
		}
	}
	want := []string{"93.184.216.34:443", "93.184.216.34:443", "93.184.216.35:443", "172.18.0.5:443"}
	if got := r.dials(); !slices.Equal(got, want) {
		t.Fatalf("dialed %v, want %v", got, want)
	}

	var denied, allowed int
	for _, e := range r.logs.entries(t) {
		switch e["msg"] {
		case "egress denied":
			denied++
			if e["host"] == "internal.cdn.test" && !strings.Contains(fmt.Sprint(e["reason"]), "address not allowed") {
				t.Errorf("reason %v", e["reason"])
			}
		case "egress":
			allowed++
			if e["method"] != "CONNECT" || e["bytes_up"].(float64) == 0 || e["bytes_down"].(float64) == 0 {
				t.Errorf("entry %v", e)
			}
		}
	}
	if allowed != 4 || denied != len(cases)-4 {
		t.Fatalf("logged %d allowed, %d denied", allowed, denied)
	}
}

func TestDNSPinning(t *testing.T) {
	r := newRig(t, upstreamServer(t, "upstream", nil, nil))
	r.dns.set("pub.cdn.test", "93.184.216.34")
	if status, _ := r.connect(t, "pub.cdn.test:443"); status != 200 {
		t.Fatalf("status %d", status)
	}
	r.dns.set("pub.cdn.test", "127.0.0.1")
	if status, _ := r.connect(t, "pub.cdn.test:443"); status != 200 {
		t.Fatalf("pinned status %d", status)
	}
	if got := r.dials(); !slices.Equal(got, []string{"93.184.216.34:443", "93.184.216.34:443"}) {
		t.Fatalf("rebinding followed: %v", got)
	}
	if n := r.dns.lookups["pub.cdn.test"]; n != 1 {
		t.Fatalf("lookups %d", n)
	}
	r.now.Add(int64(2 * time.Minute))
	if status, _ := r.connect(t, "pub.cdn.test:443"); status != 403 {
		t.Fatalf("after pin expiry status %d", status)
	}
	if len(r.dials()) != 2 {
		t.Fatalf("dialed rebinding target: %v", r.dials())
	}
}

func TestForwardDoesNotFollowRedirects(t *testing.T) {
	var targetHits, forgeHits atomic.Int32
	var sawProxyAuth atomic.Bool
	target := upstreamServer(t, "secret", &targetHits, nil)
	forge := upstreamServer(t, "", &forgeHits, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" {
			sawProxyAuth.Store(true)
		}
		http.Redirect(w, r, "http://x.cdn.test/stolen", http.StatusFound)
	})
	r := newRig(t, forge)
	r.routes["93.184.216.35:80"] = target
	r.dns.set("forge.test", "93.184.216.34")
	r.dns.set("x.cdn.test", "93.184.216.35")

	pu, _ := url.Parse("http://" + r.addr)
	client := &http.Client{
		Transport:     &http.Transport{Proxy: http.ProxyURL(pu)},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       5 * time.Second,
	}
	req, _ := http.NewRequest(http.MethodGet, "http://forge.test/x", nil)
	req.Header.Set("Proxy-Authorization", "Basic Zm9vOmJhcg==")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "http://x.cdn.test/stolen" {
		t.Fatalf("status %d location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if forgeHits.Load() != 1 || targetHits.Load() != 0 {
		t.Fatalf("forge %d target %d", forgeHits.Load(), targetHits.Load())
	}
	if sawProxyAuth.Load() {
		t.Fatal("Proxy-Authorization forwarded upstream")
	}

	for _, u := range []string{"http://evil.test/", "http://127.0.0.1/", "http://forge.test:8080/"} {
		resp, err := client.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status %d", u, resp.StatusCode)
		}
	}
	resp, err = http.Get("http://" + r.addr + "/direct")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("origin-form request status %d", resp.StatusCode)
	}
	if forgeHits.Load() != 1 || targetHits.Load() != 0 {
		t.Fatalf("denied requests reached upstream: forge %d target %d", forgeHits.Load(), targetHits.Load())
	}
}

func TestRejectsIPAllowEntries(t *testing.T) {
	for _, a := range []string{"10.0.0.1", "::1", "forge.test:443", "http://forge.test"} {
		if _, err := newProxy(options{allow: []string{a}, ports: []int{443}}); err == nil {
			t.Errorf("allow entry %q accepted", a)
		}
	}
}

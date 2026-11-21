package netguard

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type resolver map[string][]string

func (r resolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	var out []netip.Addr
	for _, s := range r[host] {
		out = append(out, netip.MustParseAddr(s))
	}
	if out == nil {
		return nil, errors.New("no such host")
	}
	return out, nil
}

type rebinder struct {
	calls atomic.Int32
	first string
	then  string
}

func (r *rebinder) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	if r.calls.Add(1) == 1 {
		return []netip.Addr{netip.MustParseAddr(r.first)}, nil
	}
	return []netip.Addr{netip.MustParseAddr(r.then)}, nil
}

type recorder struct {
	mu     sync.Mutex
	dialed []string
}

func (r *recorder) dial(_ context.Context, _, addr string) (net.Conn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dialed = append(r.dialed, addr)
	return nil, errors.New("dial stopped")
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.dialed...)
}

func testServer(t *testing.T) (port string, hits *atomic.Int32) {
	t.Helper()
	hits = new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)
	_, port, _ = net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	return port, hits
}

func TestBlockedTable(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1": true, "127.255.255.254": true, "::1": true, "0.0.0.0": true, "::": true, "0.1.2.3": true,
		"10.0.0.1": true, "172.16.0.1": true, "172.31.255.255": true, "192.168.1.1": true, "fd12:3456::1": true,
		"169.254.169.254": true, "169.254.1.1": true, "fe80::1": true, "fe80::1%eth0": true, "fd00:ec2::254": true,
		"100.64.0.1": true, "100.127.255.255": true, "100.100.100.200": true, "192.0.0.8": true, "192.0.2.1": true,
		"192.88.99.1": true, "198.18.0.1": true, "198.51.100.1": true, "203.0.113.1": true, "240.0.0.1": true,
		"255.255.255.255": true, "224.0.0.1": true, "ff02::1": true, "ff01::1": true, "2001:db8::1": true,
		"2001::1": true, "100::1": true, "fec0::1": true, "::7f00:1": true, "::a9fe:a9fe": true,
		"::ffff:127.0.0.1": true, "::ffff:10.1.2.3": true, "::ffff:169.254.169.254": true, "::ffff:0.0.0.0": true,
		"64:ff9b::7f00:1": true, "64:ff9b::a9fe:a9fe": true, "64:ff9b::a00:1": true, "64:ff9b:1::808:808": true,
		"2002:7f00:1::1": true, "2002:c0a8:101::1": true, "2002:a9fe:a9fe::": true,
		"8.8.8.8": false, "93.184.216.34": false, "172.32.0.1": false, "100.128.0.1": false, "1.1.1.1": false,
		"2606:4700:4700::1111": false, "64:ff9b::808:808": false, "2002:808:808::1": false, "::ffff:8.8.8.8": false,
	}
	for ip, want := range cases {
		if got := Blocked(netip.MustParseAddr(ip)); got != want {
			t.Errorf("Blocked(%s) = %v, want %v", ip, got, want)
		}
	}
	if !Blocked(netip.Addr{}) {
		t.Error("invalid address not blocked")
	}
}

func TestIPv4EquivalentFormsAgree(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	for range 20000 {
		var b [4]byte
		for i := range b {
			b[i] = byte(r.UintN(256))
		}
		switch r.UintN(4) {
		case 0:
			b[0] = 127
		case 1:
			b[0], b[1] = 169, 254
		case 2:
			b[0] = 10
		}
		v4 := netip.AddrFrom4(b)
		want := Blocked(v4)
		mapped := netip.AddrFrom16(v4.As16())
		var n64, s2 [16]byte
		copy(n64[:], netip.MustParseAddr("64:ff9b::").AsSlice())
		copy(n64[12:], b[:])
		s2[0], s2[1] = 0x20, 0x02
		copy(s2[2:6], b[:])
		for _, form := range []netip.Addr{mapped, netip.AddrFrom16(n64), netip.AddrFrom16(s2)} {
			if Blocked(form) != want {
				t.Fatalf("Blocked(%s) = %v but Blocked(%s) = %v", form, !want, v4, want)
			}
		}
	}
}

func TestAllowListNeverOpensMetadata(t *testing.T) {
	g := &Guard{Allow: mustPrefixes("10.0.0.0/8", "169.254.0.0/16", "127.0.0.1/32", "fd00::/8")}
	for ip, want := range map[string]bool{
		"10.2.3.4": false, "127.0.0.1": false, "127.0.0.2": true, "192.168.0.1": true,
		"169.254.169.254": true, "::ffff:169.254.169.254": true, "fd00:ec2::254": true, "169.254.1.1": false, "fd00::1": false,
	} {
		if got := g.Blocked(netip.MustParseAddr(ip)); got != want {
			t.Errorf("Blocked(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestCheckURL(t *testing.T) {
	g := &Guard{}
	cases := map[string]bool{
		"https://hooks.example.com/x?y=1":         true,
		"http://hooks.example.com:8080/":          true,
		"https://[2606:4700:4700::1111]/":         true,
		"ftp://hooks.example.com/":                false,
		"file:///etc/passwd":                      false,
		"gopher://hooks.example.com/":             false,
		"https://user:pw@hooks.example.com/":      false,
		"https://hooks.example.com@127.0.0.1/":    false,
		"https://127.0.0.1#@hooks.example.com/":   false,
		"https://127.0.0.1\\@hooks.example.com/":  false,
		"https:///path":                           false,
		"https://hooks.example.com/#frag":         false,
		"http://127.0.0.1/":                       false,
		"http://[::1]:9000/":                      false,
		"http://[::ffff:192.168.0.1]/":            false,
		"http://[64:ff9b::a9fe:a9fe]/":            false,
		"http://[fe80::1%25eth0]/":                false,
		"http://169.254.169.254/latest/meta-data": false,
		"not a url": false,
		"https://hooks.example.com/" + strings.Repeat("a", MaxURLLen): false,
		"https://hooks.example.com/" + strings.Repeat("a", 2048-26):   true,
		"https://hooks.example.com/" + strings.Repeat("a", 2049-26):   false,
	}
	for raw, ok := range cases {
		if _, err := g.CheckURL(raw); (err == nil) != ok {
			t.Errorf("CheckURL(%q) err = %v, want ok=%v", raw, err, ok)
		}
	}
}

func TestCheckHost(t *testing.T) {
	g := &Guard{}
	for host, blocked := range map[string]bool{
		"localhost": true, "LOCALHOST": true, "api.localhost": true, "localhost.": true, "127.0.0.1": true, "::1": true,
		"example.com": false, "localhost.example.com": false, "8.8.8.8": false,
	} {
		if err := g.CheckHost(host); errors.Is(err, ErrBlocked) != blocked {
			t.Errorf("CheckHost(%q) = %v, want blocked=%v", host, err, blocked)
		}
	}
	if err := g.CheckHost(""); err == nil || errors.Is(err, ErrBlocked) {
		t.Errorf("empty host: %v", err)
	}
	if err := (&Guard{AllowAll: true}).CheckHost("localhost"); err != nil {
		t.Errorf("AllowAll: %v", err)
	}
}

func TestNumericHostFormsAreCheckedAfterResolution(t *testing.T) {
	forms := []string{"2130706433", "0x7f000001", "0177.0.0.1", "0x7f.0.0.1", "127.1", "017700000001", "0", "0251.0376.0251.0376"}
	r := resolver{}
	for _, f := range forms {
		r[f] = []string{"127.0.0.1"}
	}
	r["0251.0376.0251.0376"] = []string{"169.254.169.254"}
	rec := &recorder{}
	g := &Guard{Resolver: r, Dial: rec.dial}
	for _, f := range forms {
		if _, err := g.CheckURL("http://" + f + "/"); err != nil {
			continue
		}
		if _, err := g.DialContext(context.Background(), "tcp", net.JoinHostPort(f, "80")); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: err = %v, want ErrBlocked", f, err)
		}
	}
	if d := rec.list(); len(d) != 0 {
		t.Fatalf("dialed %v", d)
	}
}

func TestDialPinsResolvedAddressAndRejectsAnyBlockedAnswer(t *testing.T) {
	rec := &recorder{}
	g := &Guard{
		Resolver: resolver{
			"public.test": {"93.184.216.34"},
			"mixed.test":  {"93.184.216.34", "10.0.0.1"},
			"mapped.test": {"::ffff:127.0.0.1"},
			"nat64.test":  {"64:ff9b::a9fe:a9fe"},
			"v6.test":     {"2606:4700:4700::1111"},
			"pub64.test":  {"::ffff:93.184.216.34"},
		},
		Dial: rec.dial,
	}
	for _, host := range []string{"mixed.test", "mapped.test", "nat64.test", "127.0.0.1", "::1", "169.254.169.254"} {
		if _, err := g.DialContext(context.Background(), "tcp", net.JoinHostPort(host, "443")); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: err = %v", host, err)
		}
	}
	if d := rec.list(); len(d) != 0 {
		t.Fatalf("dialed %v despite block", d)
	}
	for host, want := range map[string]string{
		"public.test": "93.184.216.34:443", "v6.test": "[2606:4700:4700::1111]:443", "pub64.test": "93.184.216.34:443", "8.8.8.8": "8.8.8.8:443",
	} {
		rec.dialed = nil
		_, _ = g.DialContext(context.Background(), "tcp", net.JoinHostPort(host, "443"))
		if d := rec.list(); len(d) != 1 || d[0] != want {
			t.Errorf("%s: dialed %v, want [%s]", host, d, want)
		}
	}
	if _, err := g.DialContext(context.Background(), "tcp", "nowhere.test:443"); err == nil || errors.Is(err, ErrBlocked) {
		t.Errorf("unresolvable: %v", err)
	}
}

func TestCustomErrorIsReturned(t *testing.T) {
	mine := errors.New("mine")
	g := &Guard{Err: mine}
	if _, err := g.Resolve(context.Background(), "10.0.0.1"); !errors.Is(err, mine) {
		t.Fatalf("Resolve: %v", err)
	}
	if err := g.CheckHost("localhost"); !errors.Is(err, mine) {
		t.Fatalf("CheckHost: %v", err)
	}
}

func TestDNSRebindingBetweenValidationAndDial(t *testing.T) {
	port, hits := testServer(t)
	r := &rebinder{first: "93.184.216.34", then: "127.0.0.1"}
	g := &Guard{Resolver: r}
	if _, err := g.Resolve(context.Background(), "rebind.test"); err != nil {
		t.Fatal(err)
	}
	_, err := g.Client(time.Second).Get("http://rebind.test:" + port + "/")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("receiver contacted %d times", hits.Load())
	}
}

func TestUserinfoDoesNotChangeDialTarget(t *testing.T) {
	port, hits := testServer(t)
	g := &Guard{Resolver: resolver{"public.test": {"93.184.216.34"}}}
	for _, raw := range []string{
		"http://public.test@127.0.0.1:" + port + "/",
		"http://public.test:" + port + "@127.0.0.1:" + port + "/",
		"http://user:pw@localhost:" + port + "/",
	} {
		if _, err := g.Client(time.Second).Get(raw); err == nil {
			t.Errorf("%s: request succeeded", raw)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("receiver contacted %d times", hits.Load())
	}
}

func TestRedirectsToPrivateHostsAreNotFollowed(t *testing.T) {
	port, hits := testServer(t)
	for _, target := range []string{
		"http://127.0.0.1:" + port + "/", "http://localhost:" + port + "/", "http://169.254.169.254/latest/meta-data",
		"http://[::ffff:127.0.0.1]:" + port + "/", "http://2130706433:" + port + "/",
	} {
		redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target, http.StatusFound)
		}))
		_, rport, _ := net.SplitHostPort(strings.TrimPrefix(redirector.URL, "http://"))
		g := &Guard{Resolver: resolver{"redirector.test": {"127.0.0.1"}}, Allow: mustPrefixes("127.0.0.1/32")}
		resp, err := g.Client(time.Second).Get("http://redirector.test:" + rport + "/")
		redirector.Close()
		if err != nil {
			t.Fatalf("%s: %v", target, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != target {
			t.Fatalf("%s: status %d location %q", target, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("redirect target contacted %d times", hits.Load())
	}
}

func TestAllowAllStillPinsResolution(t *testing.T) {
	port, hits := testServer(t)
	g := &Guard{AllowAll: true, Resolver: resolver{"recv.test": {"127.0.0.1"}}}
	resp, err := g.Client(time.Second).Get("http://recv.test:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if hits.Load() != 1 {
		t.Fatalf("hits %d", hits.Load())
	}
}

func TestClientIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://8.8.8.8:3128")
	t.Setenv("HTTPS_PROXY", "http://8.8.8.8:3128")
	port, hits := testServer(t)
	resp, err := New(mustPrefixes("127.0.0.0/8")).Client(time.Second).Get("http://127.0.0.1:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if hits.Load() != 1 {
		t.Fatalf("hits %d", hits.Load())
	}
}

type endless struct{ n atomic.Int64 }

func (e *endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	e.n.Add(int64(len(p)))
	return len(p), nil
}

func TestOversizedResponsesAreBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(w, io.LimitReader(&endless{}, 8<<20))
	}))
	defer srv.Close()
	resp, err := New(mustPrefixes("127.0.0.0/8")).Client(10 * time.Second).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := ReadBody(resp.Body, 1<<20)
	if err != nil || len(b) != 1<<20 {
		t.Fatalf("read %d bytes, err %v", len(b), err)
	}

	for _, limit := range []int64{0, 1, 4096, 1 << 20} {
		e := &endless{}
		Drain(e, limit)
		if got := e.n.Load(); got < limit || got > limit+32<<10 {
			t.Errorf("Drain(%d) consumed %d bytes", limit, got)
		}
		b, err := ReadBody(&endless{}, limit)
		if err != nil || int64(len(b)) != limit {
			t.Errorf("ReadBody(%d) = %d bytes, %v", limit, len(b), err)
		}
	}
	if b, _ := ReadBody(bytes.NewReader([]byte("short")), 1<<20); string(b) != "short" {
		t.Errorf("short body = %q", b)
	}
}

func TestScrubRemovesURL(t *testing.T) {
	inner := errors.New("connection refused")
	err := &url.Error{Op: "Post", URL: "https://hooks.example.com/T0/secret-part?token=abc", Err: inner}
	if got := Scrub(err); got != inner || strings.Contains(got.Error(), "secret-part") {
		t.Fatalf("Scrub = %v", got)
	}
	if got := Scrub(inner); got != inner {
		t.Fatalf("Scrub(plain) = %v", got)
	}
	port, _ := testServer(t)
	_, gerr := New(nil).Client(time.Second).Get("http://127.0.0.1:" + port + "/secret-part?token=abc")
	if s := Scrub(gerr).Error(); strings.Contains(s, "secret-part") || strings.Contains(s, "token") || !errors.Is(gerr, ErrBlocked) {
		t.Fatalf("scrubbed = %q", s)
	}
}

func TestParsePrefixes(t *testing.T) {
	got, err := ParsePrefixes(" 10.0.0.0/8, 192.168.1.7 ,, ::1, 10.1.2.3/8 ")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.0/8", "192.168.1.7/32", "::1/128", "10.0.0.0/8"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if _, err := ParsePrefixes("10.0.0.0/8,nope"); err == nil {
		t.Fatal("invalid entry accepted")
	}
}

type deadlineResolver struct {
	addrs       []netip.Addr
	err         error
	hasDeadline bool
}

func (r *deadlineResolver) LookupNetIP(ctx context.Context, _, _ string) ([]netip.Addr, error) {
	_, r.hasDeadline = ctx.Deadline()
	return r.addrs, r.err
}

func TestResolveLookupErrorsAndTimeout(t *testing.T) {
	down := errors.New("dns down")
	r := &deadlineResolver{err: down}
	g := &Guard{Resolver: r}
	if _, err := g.Resolve(context.Background(), "a.test"); !errors.Is(err, down) {
		t.Fatalf("lookup error = %v", err)
	}
	if r.hasDeadline {
		t.Fatal("deadline applied without LookupTimeout")
	}
	g.LookupTimeout = time.Minute
	r.err, r.addrs = nil, []netip.Addr{}
	ips, err := g.Resolve(context.Background(), "a.test")
	if err == nil || errors.Is(err, ErrBlocked) || ips != nil {
		t.Fatalf("empty answer = %v, %v", ips, err)
	}
	if !r.hasDeadline {
		t.Fatal("LookupTimeout not applied")
	}
	if _, err := g.DialContext(context.Background(), "tcp", "no-port"); err == nil || !strings.Contains(err.Error(), "missing port") {
		t.Fatalf("bad addr = %v", err)
	}
}

func TestTransportHandshakeTimeout(t *testing.T) {
	if got := (&Guard{}).Transport().TLSHandshakeTimeout; got != 10*time.Second {
		t.Fatalf("default = %v", got)
	}
	if got := (&Guard{DialTimeout: 3 * time.Second}).Transport().TLSHandshakeTimeout; got != 3*time.Second {
		t.Fatalf("custom = %v", got)
	}
}

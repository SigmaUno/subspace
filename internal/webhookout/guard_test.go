package webhookout

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
)

type fakeResolver map[string][]string

func (r fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	var out []netip.Addr
	for _, s := range r[host] {
		out = append(out, netip.MustParseAddr(s))
	}
	if out == nil {
		return nil, errors.New("no such host")
	}
	return out, nil
}

func TestBlockedAddresses(t *testing.T) {
	g := &Guard{}
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true}, {"127.9.9.9", true}, {"::1", true},
		{"10.0.0.1", true}, {"172.16.5.4", true}, {"172.31.255.255", true}, {"192.168.1.1", true}, {"fd12:3456::1", true},
		{"169.254.169.254", true}, {"169.254.1.1", true}, {"fe80::1", true}, {"fd00:ec2::254", true},
		{"0.0.0.0", true}, {"::", true}, {"0.1.2.3", true}, {"100.64.0.1", true}, {"100.127.255.255", true},
		{"224.0.0.1", true}, {"ff02::1", true}, {"255.255.255.255", true}, {"240.0.0.1", true},
		{"198.18.0.1", true}, {"192.0.2.1", true}, {"2001:db8::1", true}, {"fec0::1", true},
		{"::ffff:127.0.0.1", true}, {"::ffff:10.1.2.3", true}, {"::ffff:169.254.169.254", true},
		{"64:ff9b::7f00:1", true}, {"64:ff9b::a9fe:a9fe", true}, {"2002:7f00:1::1", true}, {"2002:c0a8:101::1", true},
		{"8.8.8.8", false}, {"93.184.216.34", false}, {"172.32.0.1", false}, {"100.128.0.1", false},
		{"2606:4700:4700::1111", false}, {"64:ff9b::808:808", false}, {"2002:0808:0808::1", false}, {"::ffff:8.8.8.8", false},
	}
	for _, c := range cases {
		if got := g.Blocked(netip.MustParseAddr(c.ip)); got != c.blocked {
			t.Errorf("Blocked(%s) = %v, want %v", c.ip, got, c.blocked)
		}
	}
}

func TestAllowListNeverOpensMetadata(t *testing.T) {
	g := &Guard{Allow: mustPrefixes("10.0.0.0/8", "169.254.0.0/16", "127.0.0.1/32")}
	for ip, want := range map[string]bool{"10.2.3.4": false, "127.0.0.1": false, "127.0.0.2": true, "192.168.0.1": true, "169.254.169.254": true, "169.254.1.1": false} {
		if got := g.Blocked(netip.MustParseAddr(ip)); got != want {
			t.Errorf("Blocked(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestCheckURL(t *testing.T) {
	g := &Guard{}
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://hooks.example.com/x?y=1", true},
		{"http://hooks.example.com:8080/", true},
		{"ftp://hooks.example.com/", false},
		{"file:///etc/passwd", false},
		{"https://user:pw@hooks.example.com/", false},
		{"https:///path", false},
		{"https://hooks.example.com/#frag", false},
		{"http://127.0.0.1/", false},
		{"http://[::1]:9000/", false},
		{"http://169.254.169.254/latest/meta-data", false},
		{"http://10.0.0.5/", false},
		{"http://[::ffff:192.168.0.1]/", false},
		{"not a url", false},
	}
	for _, c := range cases {
		if _, err := g.CheckURL(c.url); (err == nil) != c.ok {
			t.Errorf("CheckURL(%q) err = %v, want ok=%v", c.url, err, c.ok)
		}
	}
}

func TestDialResolvesAndPinsAddress(t *testing.T) {
	var dialed []string
	g := &Guard{
		Resolver: fakeResolver{
			"public.test":  {"93.184.216.34"},
			"private.test": {"10.0.0.7"},
			"mixed.test":   {"93.184.216.34", "127.0.0.1"},
			"meta.test":    {"169.254.169.254"},
			"v6.test":      {"2606:4700:4700::1111"},
		},
		Dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			dialed = append(dialed, addr)
			return nil, errors.New("dial stopped")
		},
	}
	for _, host := range []string{"private.test", "mixed.test", "meta.test", "127.0.0.1", "[::1]"} {
		dialed = nil
		if _, err := g.DialContext(context.Background(), "tcp", net.JoinHostPort(trimBrackets(host), "443")); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: err = %v, want ErrBlocked", host, err)
		}
		if len(dialed) != 0 {
			t.Errorf("%s: dialed %v despite block", host, dialed)
		}
	}
	for host, want := range map[string]string{"public.test": "93.184.216.34:443", "v6.test": "[2606:4700:4700::1111]:443", "8.8.8.8": "8.8.8.8:443"} {
		dialed = nil
		_, _ = g.DialContext(context.Background(), "tcp", net.JoinHostPort(host, "443"))
		if len(dialed) != 1 || dialed[0] != want {
			t.Errorf("%s: dialed %v, want [%s]", host, dialed, want)
		}
	}
	if _, err := g.DialContext(context.Background(), "tcp", "nowhere.test:443"); err == nil || errors.Is(err, ErrBlocked) {
		t.Errorf("unresolvable host: err = %v", err)
	}
}

func trimBrackets(s string) string {
	if len(s) > 2 && s[0] == '[' {
		return s[1 : len(s)-1]
	}
	return s
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()
	g := NewGuard(mustPrefixes("127.0.0.0/8"))
	resp, err := g.Client(requestTimeout).Post(redirector.URL, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || hits.Load() != 0 {
		t.Fatalf("status %d, redirect target hit %d times", resp.StatusCode, hits.Load())
	}
}

func TestClientRefusesLoopbackByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("receiver reached") }))
	defer srv.Close()
	if resp, err := NewGuard(nil).Client(requestTimeout).Post(srv.URL, "application/json", nil); err == nil {
		_ = resp.Body.Close()
		t.Fatal("loopback receiver was reachable")
	} else if !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
}

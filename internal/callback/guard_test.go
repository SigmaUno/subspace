package callback

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeResolver map[string][]netip.Addr

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if a, ok := f[host]; ok {
		return a, nil
	}
	return nil, errors.New("no such host")
}

func addrs(s ...string) []netip.Addr {
	var out []netip.Addr
	for _, v := range s {
		out = append(out, netip.MustParseAddr(v))
	}
	return out
}

func TestValidateTable(t *testing.T) {
	p := Policy{Resolver: fakeResolver{
		"example.com":     addrs("93.184.216.34"),
		"internal.corp":   addrs("10.0.0.1"),
		"mixed.example":   addrs("93.184.216.34", "127.0.0.1"),
		"meta.example":    addrs("169.254.169.254"),
		"mapped.example":  addrs("::ffff:127.0.0.1"),
		"v6private.corp":  addrs("fd12:3456::1"),
		"cgnat.example":   addrs("100.64.1.1"),
		"nat64.example":   addrs("64:ff9b::7f00:1"),
		"empty.example":   nil,
		"dualstack.pub":   addrs("93.184.216.34", "2606:4700::1111"),
		"loopback6.local": addrs("::1"),
	}}
	long := "https://example.com/" + strings.Repeat("a", MaxURLLen)
	cases := []struct {
		url     string
		ok      bool
		blocked bool
	}{
		{"https://example.com/hook", true, false},
		{"https://example.com:8443/hook?x=1", true, false},
		{"https://dualstack.pub/", true, false},
		{"https://8.8.8.8/", true, false},
		{"https://[2606:4700::1111]/", true, false},
		{"http://example.com/hook", false, false},
		{"ftp://example.com/", false, false},
		{"//example.com/", false, false},
		{"example.com/hook", false, false},
		{"https://user:pw@example.com/", false, false},
		{"https:///path", false, false},
		{"https://unresolvable.example/", false, false},
		{"https://empty.example/", false, false},
		{"https://2130706433/", false, false},
		{long, false, false},
		{"https://localhost/", false, true},
		{"https://LOCALHOST:8080/", false, true},
		{"https://api.localhost/", false, true},
		{"https://127.0.0.1/", false, true},
		{"https://127.1.2.3:9999/", false, true},
		{"https://[::1]/", false, true},
		{"https://0.0.0.0/", false, true},
		{"https://[::]/", false, true},
		{"https://10.0.0.5/", false, true},
		{"https://172.16.0.1/", false, true},
		{"https://172.31.255.255/", false, true},
		{"https://192.168.1.1/", false, true},
		{"https://169.254.169.254/latest/meta-data", false, true},
		{"https://[fd00:ec2::254]/", false, true},
		{"https://[fe80::1]/", false, true},
		{"https://100.100.100.200/", false, true},
		{"https://100.64.0.1/", false, true},
		{"https://[::ffff:127.0.0.1]/", false, true},
		{"https://[::ffff:10.0.0.1]/", false, true},
		{"https://[::ffff:169.254.169.254]/", false, true},
		{"https://224.0.0.1/", false, true},
		{"https://255.255.255.255/", false, true},
		{"https://198.18.0.1/", false, true},
		{"https://[64:ff9b::7f00:1]/", false, true},
		{"https://internal.corp/", false, true},
		{"https://mixed.example/", false, true},
		{"https://meta.example/", false, true},
		{"https://mapped.example/", false, true},
		{"https://v6private.corp/", false, true},
		{"https://cgnat.example/", false, true},
		{"https://nat64.example/", false, true},
		{"https://loopback6.local/", false, true},
	}
	for _, c := range cases {
		err := p.Validate(context.Background(), c.url)
		if (err == nil) != c.ok {
			t.Errorf("%s: ok=%v err=%v", c.url, c.ok, err)
			continue
		}
		if c.blocked && !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: want ErrBlocked, got %v", c.url, err)
		}
	}
}

func TestAllowPrivateAcceptsHTTPLoopback(t *testing.T) {
	p := Policy{AllowPrivate: true, Resolver: fakeResolver{}}
	if err := p.Validate(context.Background(), "http://127.0.0.1:9/x"); err != nil {
		t.Fatal(err)
	}
}

type rebindResolver struct {
	calls atomic.Int32
	first netip.Addr
	then  netip.Addr
}

func (r *rebindResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	if r.calls.Add(1) == 1 {
		return []netip.Addr{r.first}, nil
	}
	return []netip.Addr{r.then}, nil
}

func TestDialerRechecksResolutionAndNeverConnectsToPrivate(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	_, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	r := &rebindResolver{first: netip.MustParseAddr("93.184.216.34"), then: netip.MustParseAddr("127.0.0.1")}
	p := Policy{Resolver: r}
	if err := p.Validate(context.Background(), "https://rebind.example:"+port+"/"); err != nil {
		t.Fatal(err)
	}
	_, err := p.NewClient(time.Second).Get("http://rebind.example:" + port + "/")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("want ErrBlocked, got %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("receiver was contacted %d times", hits.Load())
	}
}

func TestDialerPinsToResolvedAddressAndIgnoresRedirects(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "http://127.0.0.1:1/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	_, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	p := Policy{AllowPrivate: true, Resolver: fakeResolver{"recv.example": addrs("127.0.0.1")}}
	resp, err := p.NewClient(time.Second).Get("http://recv.example:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || hits.Load() != 1 {
		t.Fatalf("status %d hits %d", resp.StatusCode, hits.Load())
	}
}

package faultnet_test

import (
	"io"
	"net"
	"slices"
	"testing"

	"github.com/SigmaUno/subspace/internal/faultnet"
)

func echo(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

func outcomes(t *testing.T, target string, seed uint64) []int {
	t.Helper()
	p, err := faultnet.Listen(target, seed, faultnet.Faults{ResetDown: 0.25, Truncate: 0.25})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	msg := make([]byte, 100)
	var out []int
	for range 40 {
		c, err := net.Dial("tcp", p.Addr())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Write(msg); err != nil {
			t.Fatal(err)
		}
		_ = c.(*net.TCPConn).CloseWrite()
		n, _ := io.ReadFull(c, make([]byte, len(msg)))
		_ = c.Close()
		out = append(out, n)
	}
	return out
}

func TestSeededFaultsAreDeterministic(t *testing.T) {
	target := echo(t)
	a, b := outcomes(t, target, 1), outcomes(t, target, 1)
	if !slices.Equal(a, b) {
		t.Fatalf("same seed, different faults:\n%v\n%v", a, b)
	}
	if slices.Equal(a, outcomes(t, target, 2)) {
		t.Fatal("different seeds produced identical faults")
	}
	var ok, cut, reset int
	for _, n := range a {
		switch {
		case n == 100:
			ok++
		case n > 0:
			cut++
		default:
			reset++
		}
	}
	if ok == 0 || cut == 0 || reset == 0 {
		t.Fatalf("faults not mixed: %d delivered, %d truncated, %d reset", ok, cut, reset)
	}
}

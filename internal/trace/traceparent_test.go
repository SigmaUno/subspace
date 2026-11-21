package trace

import (
	"net/http"
	"strings"
	"testing"
)

const (
	goodTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	goodSpan  = "00f067aa0ba902b7"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name   string
		header string
		ok     bool
		flags  byte
	}{
		{"sampled", "00-" + goodTrace + "-" + goodSpan + "-01", true, 1},
		{"unsampled", "00-" + goodTrace + "-" + goodSpan + "-00", true, 0},
		{"unknown flag bits kept", "00-" + goodTrace + "-" + goodSpan + "-09", true, 9},
		{"future version with extra fields", "01-" + goodTrace + "-" + goodSpan + "-01-extra-data", true, 1},
		{"future version exact length", "7f-" + goodTrace + "-" + goodSpan + "-01", true, 1},
		{"version 00 with trailing fields", "00-" + goodTrace + "-" + goodSpan + "-01-extra", false, 0},
		{"future version trailing without dash", "01-" + goodTrace + "-" + goodSpan + "-01x", false, 0},
		{"version ff", "ff-" + goodTrace + "-" + goodSpan + "-01", false, 0},
		{"uppercase trace id", "00-" + strings.ToUpper(goodTrace) + "-" + goodSpan + "-01", false, 0},
		{"uppercase version", "0A-" + goodTrace + "-" + goodSpan + "-01", false, 0},
		{"zero trace id", "00-00000000000000000000000000000000-" + goodSpan + "-01", false, 0},
		{"zero span id", "00-" + goodTrace + "-0000000000000000-01", false, 0},
		{"short trace id", "00-" + goodTrace[:30] + "-" + goodSpan + "-01", false, 0},
		{"non hex", "00-" + goodTrace[:31] + "g-" + goodSpan + "-01", false, 0},
		{"missing dash", "00_" + goodTrace + "-" + goodSpan + "-01", false, 0},
		{"leading space", " 00-" + goodTrace + "-" + goodSpan + "-01", false, 0},
		{"trailing space", "00-" + goodTrace + "-" + goodSpan + "-01 ", false, 0},
		{"empty", "", false, 0},
		{"too short", "00-" + goodTrace + "-" + goodSpan, false, 0},
		{"flags not hex", "00-" + goodTrace + "-" + goodSpan + "-zz", false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sc, ok := Parse(c.header)
			if ok != c.ok {
				t.Fatalf("Parse(%q) ok = %v, want %v", c.header, ok, c.ok)
			}
			if !ok {
				return
			}
			if sc.TraceID.String() != goodTrace || sc.SpanID.String() != goodSpan || sc.Flags != c.flags || !sc.Propagated {
				t.Fatalf("Parse(%q) = %+v", c.header, sc)
			}
		})
	}
}

func TestTraceparentRoundTrip(t *testing.T) {
	sc, ok := Parse("00-" + goodTrace + "-" + goodSpan + "-01")
	if !ok {
		t.Fatal("parse")
	}
	if got := sc.Traceparent(); got != "00-"+goodTrace+"-"+goodSpan+"-01" {
		t.Fatalf("Traceparent() = %q", got)
	}
	sc, _ = Parse("03-" + goodTrace + "-" + goodSpan + "-01-future")
	if got := sc.Traceparent(); got != "00-"+goodTrace+"-"+goodSpan+"-01" {
		t.Fatalf("future version is re-emitted as 00: %q", got)
	}
}

func TestFromHeader(t *testing.T) {
	good := "00-" + goodTrace + "-" + goodSpan + "-01"
	h := http.Header{}
	h.Add("Traceparent", good)
	h.Add("Tracestate", "vendor=a")
	h.Add("Tracestate", "other=b")
	sc, ok := FromHeader(h)
	if !ok || sc.State != "vendor=a,other=b" {
		t.Fatalf("FromHeader = %+v, %v", sc, ok)
	}

	dup := http.Header{}
	dup.Add("Traceparent", good)
	dup.Add("Traceparent", "00-"+strings.Repeat("a", 32)+"-"+goodSpan+"-01")
	if _, ok := FromHeader(dup); ok {
		t.Fatal("duplicate traceparent headers must be discarded")
	}

	bad := http.Header{}
	bad.Set("Traceparent", good)
	bad.Set("Tracestate", "k=\x01v")
	sc, ok = FromHeader(bad)
	if !ok || sc.State != "" {
		t.Fatalf("invalid tracestate must be dropped without invalidating traceparent: %+v %v", sc, ok)
	}

	long := http.Header{}
	long.Set("Traceparent", good)
	long.Set("Tracestate", strings.Repeat("a=b,", 200))
	if sc, _ := FromHeader(long); sc.State != "" {
		t.Fatal("oversized tracestate must be dropped")
	}
}

func FuzzParse(f *testing.F) {
	f.Add("00-" + goodTrace + "-" + goodSpan + "-01")
	f.Add("01-" + goodTrace + "-" + goodSpan + "-01-x")
	f.Add("")
	f.Fuzz(func(t *testing.T, h string) {
		sc, ok := Parse(h)
		if !ok {
			return
		}
		again, ok := Parse(sc.Traceparent())
		if !ok || again.TraceID != sc.TraceID || again.SpanID != sc.SpanID || again.Flags != sc.Flags {
			t.Fatalf("round trip of %q failed: %+v vs %+v", h, sc, again)
		}
	})
}

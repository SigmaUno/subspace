package trace

import (
	"context"
	"encoding/hex"
	"net/http"
	"strings"
)

const (
	HeaderTraceparent = "Traceparent"
	HeaderTracestate  = "Tracestate"
	HeaderTraceID     = "X-Trace-Id"

	FlagSampled byte = 0x01

	traceparentLen = 55
	maxTracestate  = 512
)

type ID [16]byte

type SpanID [8]byte

func (id ID) IsZero() bool { return id == ID{} }

func (id ID) String() string { return hex.EncodeToString(id[:]) }

func (id SpanID) IsZero() bool { return id == SpanID{} }

func (id SpanID) String() string { return hex.EncodeToString(id[:]) }

type SpanContext struct {
	TraceID    ID
	SpanID     SpanID
	Flags      byte
	State      string
	Propagated bool
}

func (c SpanContext) Valid() bool { return !c.TraceID.IsZero() && !c.SpanID.IsZero() }

func (c SpanContext) Sampled() bool { return c.Flags&FlagSampled != 0 }

func (c SpanContext) Traceparent() string {
	var b [traceparentLen]byte
	copy(b[:], "00-")
	hex.Encode(b[3:35], c.TraceID[:])
	b[35] = '-'
	hex.Encode(b[36:52], c.SpanID[:])
	b[52] = '-'
	hex.Encode(b[53:], []byte{c.Flags})
	return string(b[:])
}

func Parse(h string) (SpanContext, bool) {
	if len(h) < traceparentLen {
		return SpanContext{}, false
	}
	if h[2] != '-' || h[35] != '-' || h[52] != '-' {
		return SpanContext{}, false
	}
	version, ok := lowerHex(h[:2])
	if !ok || version[0] == 0xff {
		return SpanContext{}, false
	}
	if version[0] == 0 && len(h) != traceparentLen {
		return SpanContext{}, false
	}
	if len(h) > traceparentLen && h[traceparentLen] != '-' {
		return SpanContext{}, false
	}
	var c SpanContext
	tid, ok := lowerHex(h[3:35])
	if !ok {
		return SpanContext{}, false
	}
	sid, ok := lowerHex(h[36:52])
	if !ok {
		return SpanContext{}, false
	}
	flags, ok := lowerHex(h[53:55])
	if !ok {
		return SpanContext{}, false
	}
	copy(c.TraceID[:], tid)
	copy(c.SpanID[:], sid)
	c.Flags = flags[0]
	c.Propagated = true
	if !c.Valid() {
		return SpanContext{}, false
	}
	return c, true
}

func lowerHex(s string) ([]byte, bool) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return nil, false
		}
	}
	b, err := hex.DecodeString(s)
	return b, err == nil
}

func ParseID(s string) (ID, bool) {
	var id ID
	if len(s) != 32 {
		return id, false
	}
	b, ok := lowerHex(s)
	if !ok {
		return id, false
	}
	copy(id[:], b)
	return id, !id.IsZero()
}

func FromHeader(h http.Header) (SpanContext, bool) {
	vals := h.Values(HeaderTraceparent)
	if len(vals) != 1 {
		return SpanContext{}, false
	}
	c, ok := Parse(vals[0])
	if !ok {
		return SpanContext{}, false
	}
	c.State = State(h.Values(HeaderTracestate)...)
	return c, true
}

func State(vals ...string) string {
	s := strings.Join(vals, ",")
	if s == "" || len(s) > maxTracestate {
		return ""
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return ""
		}
	}
	return s
}

func Inject(h http.Header, c SpanContext) {
	if !c.Valid() {
		return
	}
	h.Set(HeaderTraceparent, c.Traceparent())
	if c.State != "" {
		h.Set(HeaderTracestate, c.State)
	}
}

func Adopt(ctx context.Context, h http.Header) context.Context {
	if From(ctx).Valid() {
		return ctx
	}
	if c, ok := FromHeader(h); ok {
		return WithSpanContext(ctx, c)
	}
	return ctx
}

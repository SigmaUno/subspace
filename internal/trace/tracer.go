package trace

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"time"
)

type Kind int

const (
	KindInternal Kind = 1
	KindServer   Kind = 2
	KindClient   Kind = 3
)

type Attr struct {
	Key   string
	Value any
}

func String(k, v string) Attr { return Attr{k, v} }

func Int(k string, v int) Attr { return Attr{k, int64(v)} }

func Bool(k string, v bool) Attr { return Attr{k, v} }

type Link struct {
	TraceID ID
	SpanID  SpanID
}

type SpanData struct {
	TraceID  ID
	SpanID   SpanID
	ParentID SpanID
	State    string
	Name     string
	Kind     Kind
	Start    time.Time
	End      time.Time
	Attrs    []Attr
	Links    []Link
	Failed   bool
	Message  string
}

type Exporter interface {
	Export(*SpanData)
}

type Tracer struct {
	exp       Exporter
	now       func() time.Time
	threshold uint64
}

func NewTracer(ratio float64, exp Exporter) *Tracer {
	t := &Tracer{exp: exp, now: time.Now}
	if exp != nil {
		t.threshold = threshold(ratio)
	}
	return t
}

func threshold(ratio float64) uint64 {
	switch {
	case ratio <= 0:
		return 0
	case ratio >= 1:
		return 1 << 63
	}
	return uint64(ratio * (1 << 63))
}

func (t *Tracer) SampledID(id ID) bool {
	return binary.BigEndian.Uint64(id[8:])>>1 < t.threshold
}

type (
	tracerKey struct{}
	spanKey   struct{}
)

func WithTracer(ctx context.Context, t *Tracer) context.Context {
	return context.WithValue(ctx, tracerKey{}, t)
}

func TracerFrom(ctx context.Context) *Tracer {
	t, _ := ctx.Value(tracerKey{}).(*Tracer)
	return t
}

func WithSpanContext(ctx context.Context, c SpanContext) context.Context {
	return context.WithValue(ctx, spanKey{}, c)
}

func From(ctx context.Context) SpanContext {
	c, _ := ctx.Value(spanKey{}).(SpanContext)
	return c
}

func TraceID(ctx context.Context) string {
	if c := From(ctx); !c.TraceID.IsZero() {
		return c.TraceID.String()
	}
	return ""
}

func Recorded(ctx context.Context) string {
	if c := From(ctx); !c.TraceID.IsZero() && (c.Propagated || c.Sampled()) {
		return c.TraceID.String()
	}
	return ""
}

func newID() ID {
	var id ID
	for id.IsZero() {
		_, _ = rand.Read(id[:])
	}
	return id
}

func newSpanID() SpanID {
	var id SpanID
	for id.IsZero() {
		_, _ = rand.Read(id[:])
	}
	return id
}

type Span struct {
	t    *Tracer
	data SpanData
	sc   SpanContext
}

func (s *Span) Context() SpanContext {
	if s == nil {
		return SpanContext{}
	}
	return s.sc
}

func (s *Span) SetName(name string) {
	if s != nil {
		s.data.Name = name
	}
}

func (s *Span) SetAttrs(attrs ...Attr) {
	if s != nil {
		s.data.Attrs = append(s.data.Attrs, attrs...)
	}
}

func (s *Span) AddLink(c SpanContext) {
	if s != nil && c.Valid() {
		s.data.Links = append(s.data.Links, Link{c.TraceID, c.SpanID})
	}
}

func (s *Span) Fail(msg string) {
	if s != nil {
		s.data.Failed, s.data.Message = true, msg
	}
}

func (s *Span) End() {
	if s == nil || s.t == nil {
		return
	}
	s.data.End = s.t.now()
	t := s.t
	s.t = nil
	t.exp.Export(&s.data)
}

func (t *Tracer) recording(c SpanContext) bool { return t.exp != nil && c.Sampled() }

func (t *Tracer) begin(ctx context.Context, parent SpanContext, decided bool, name string, kind Kind, attrs []Attr) (context.Context, *Span) {
	if !parent.Valid() {
		parent = SpanContext{TraceID: parent.TraceID, Flags: parent.Flags, State: parent.State}
		if parent.TraceID.IsZero() {
			parent.TraceID = newID()
			parent.Flags = 0
			decided = false
		}
		if !decided && t.SampledID(parent.TraceID) {
			parent.Flags = FlagSampled
		}
		parent.SpanID = newSpanID()
		if !t.recording(parent) {
			return WithSpanContext(ctx, parent), nil
		}
		return t.open(ctx, parent, SpanID{}, name, kind, attrs)
	}
	if !t.recording(parent) {
		return WithSpanContext(ctx, parent), nil
	}
	return t.open(ctx, parent, parent.SpanID, name, kind, attrs)
}

func (t *Tracer) open(ctx context.Context, parent SpanContext, parentID SpanID, name string, kind Kind, attrs []Attr) (context.Context, *Span) {
	sc := parent
	sc.SpanID = newSpanID()
	s := &Span{t: t, sc: sc, data: SpanData{
		TraceID: sc.TraceID, SpanID: sc.SpanID, ParentID: parentID, State: sc.State,
		Name: name, Kind: kind, Start: t.now(), Attrs: attrs,
	}}
	return WithSpanContext(ctx, sc), s
}

func Start(ctx context.Context, name string, kind Kind, attrs ...Attr) (context.Context, *Span) {
	c := From(ctx)
	if !c.Sampled() {
		return ctx, nil
	}
	t := TracerFrom(ctx)
	if t == nil || t.exp == nil {
		return ctx, nil
	}
	return t.open(ctx, c, c.SpanID, name, kind, attrs)
}

func StartRoot(ctx context.Context, name string, kind Kind, attrs ...Attr) (context.Context, *Span) {
	t := TracerFrom(ctx)
	if t == nil {
		return ctx, nil
	}
	return t.begin(ctx, SpanContext{}, false, name, kind, attrs)
}

func StartRemote(ctx context.Context, remote SpanContext, name string, kind Kind, attrs ...Attr) (context.Context, *Span) {
	t := TracerFrom(ctx)
	if t == nil {
		if remote.Valid() {
			ctx = WithSpanContext(ctx, remote)
		}
		return ctx, nil
	}
	return t.begin(ctx, remote, false, name, kind, attrs)
}

func Continue(ctx context.Context, traceID string, flags byte, name string, kind Kind, attrs ...Attr) (context.Context, *Span) {
	t := TracerFrom(ctx)
	if t == nil {
		return ctx, nil
	}
	id, ok := ParseID(traceID)
	return t.begin(ctx, SpanContext{TraceID: id, Flags: flags}, ok, name, kind, attrs)
}

func (s *Span) SetStart(at time.Time) {
	if s != nil {
		s.data.Start = at
	}
}

type Batch struct {
	first   context.Context
	links   []SpanContext
	sampled bool
}

func (b *Batch) Add(ctx context.Context) {
	c := From(ctx)
	if !c.Sampled() {
		return
	}
	if !b.sampled {
		b.first, b.sampled = ctx, true
		return
	}
	b.links = append(b.links, c)
}

func (b *Batch) End(name string, start time.Time, err error, attrs ...Attr) {
	if !b.sampled {
		return
	}
	_, span := Start(b.first, name, KindInternal, attrs...)
	if span == nil {
		return
	}
	span.SetStart(start)
	for _, l := range b.links {
		span.AddLink(l)
	}
	if err != nil {
		span.Fail(err.Error())
	}
	span.End()
}

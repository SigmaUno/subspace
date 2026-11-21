package events

import (
	"time"

	"github.com/SigmaUno/subspace/internal/trace"
)

type Event struct {
	Seq  int64     `json:"-"`
	Type string    `json:"type"`
	TS   time.Time `json:"ts"`
	Data any       `json:"data"`

	Trace trace.SpanContext `json:"-"`

	enc *encoded
}

type Publisher interface {
	Publish(topics []string, typ string, data any)
}

type TracePublisher interface {
	PublishTrace(topics []string, typ string, data any, tc trace.SpanContext)
}

func PublishTrace(p Publisher, topics []string, typ string, data any, tc trace.SpanContext) {
	if tp, ok := p.(TracePublisher); ok && !tc.TraceID.IsZero() {
		tp.PublishTrace(topics, typ, data, tc)
		return
	}
	p.Publish(topics, typ, data)
}

type Subscription interface {
	C() <-chan Event

	TakeDropped() int
	Close()
}

type Hub interface {
	Publisher

	Subscribe(topics []string) Subscription
	Subscribers() int
}

type QuietSubscriber interface {
	SubscribeQuiet(topics []string) Subscription
}

func SubscribeQuiet(h Hub, topics []string) Subscription {
	if q, ok := h.(QuietSubscriber); ok {
		return q.SubscribeQuiet(topics)
	}
	return h.Subscribe(topics)
}

type Resumed struct {
	Sub    Subscription
	Replay []Event
	Resync bool
	Seq    int64
}

type Resumer interface {
	Hub

	Resume(topics []string, after int64) Resumed
}

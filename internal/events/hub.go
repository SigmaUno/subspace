package events

import "time"

type Event struct {
	Seq  int64     `json:"-"`
	Type string    `json:"type"`
	TS   time.Time `json:"ts"`
	Data any       `json:"data"`

	enc *encoded
}

type Publisher interface {
	Publish(topics []string, typ string, data any)
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

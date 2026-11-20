package events

import (
	"sync"
	"time"
)

type Event struct {
	Seq  int64     `json:"-"`
	Type string    `json:"type"`
	TS   time.Time `json:"ts"`
	Data any       `json:"data"`

	enc *encoded
}

type encoded struct {
	once sync.Once
	b    []byte
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

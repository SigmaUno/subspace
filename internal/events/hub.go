// Package events is the SSE fan-out hub (topic-filtered, slow-client coalescing).
package events

import "time"

// Event is one SSE event.
type Event struct {
	Seq  int64     `json:"-"`
	Type string    `json:"type"`
	TS   time.Time `json:"ts"`
	Data any       `json:"data"`
}

// Publisher publishes events to topics.
type Publisher interface {
	Publish(topics []string, typ string, data any)
}

// Subscription is a consumer of events.
type Subscription interface {
	C() <-chan Event
	// TakeDropped returns and resets the number of events dropped for this slow subscriber.
	TakeDropped() int
	Close()
}

// Hub fans events out to subscribers.
type Hub interface {
	Publisher
	// Subscribe registers for the topics ("all" matches everything).
	Subscribe(topics []string) Subscription
	Subscribers() int
}

// TODO(split B1): implement NewHub() Hub in hub_impl.go.

package webhookout

import (
	"encoding/json"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	StatePending   = "pending"
	StateDelivered = "delivered"
	StateDead      = "dead"
)

type Subscription struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Events    []string  `json:"events"`
	BriefID   *string   `json:"brief_id"`
	Pending   int       `json:"pending"`
	Delivered int       `json:"delivered"`
	Dead      int       `json:"dead"`
	CreatedAt time.Time `json:"created_at"`

	Secret string `json:"-"`
}

type Delivery struct {
	ID            string          `json:"id"`
	WebhookID     string          `json:"webhook_id"`
	Event         string          `json:"event"`
	State         string          `json:"state"`
	Attempts      int             `json:"attempts"`
	NextAttemptAt *time.Time      `json:"next_attempt_at"`
	LastStatus    *int            `json:"last_status"`
	LastError     string          `json:"last_error"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	DeliveredAt   *time.Time      `json:"delivered_at"`
	Payload       json.RawMessage `json:"payload"`
}

type Due struct {
	Delivery
	URL    string
	Secret string
}

type AuditFn func() *model.AuditEvent

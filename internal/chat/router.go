package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	targetAgent     = "agent"
	targetComponent = "component"
	targetBrief     = "brief"
	targetLeader    = "leader"
)

type Message struct {
	To   string `json:"to"`
	Body string `json:"body"`
}

type Entry struct {
	Message model.Message
	ConvID  string
}

type Directory interface {
	Agent(ctx context.Context, id string) (*model.Agent, error)
	ByComponent(ctx context.Context, id string) ([]string, error)
	ByBrief(ctx context.Context, id string) ([]string, error)
	Leader(ctx context.Context) (string, error)
}

type Threads interface {
	Append(ctx context.Context, agentID, convID string, m model.Message) (model.Message, error)
	Messages(ctx context.Context, agentID string, sinceID int64) ([]Entry, error)
}

type Config struct {
	Directory Directory
	Threads   Threads
	Now       func() time.Time
	NewID     func() string
}

type Conversation struct {
	ID         string          `json:"id"`
	To         string          `json:"to"`
	Recipients []string        `json:"recipients"`
	CreatedAt  time.Time       `json:"created_at"`
	Messages   []model.Message `json:"messages,omitempty"`
}

type Router struct {
	dir     Directory
	threads Threads
	now     func() time.Time
	newID   func() string

	mu     sync.Mutex
	convs  map[string]Conversation
	order  []string
	unread map[string]int
}

func NewRouter(c Config) *Router {
	r := &Router{
		dir:     c.Directory,
		threads: c.Threads,
		now:     c.Now,
		newID:   c.NewID,
		convs:   make(map[string]Conversation),
		unread:  make(map[string]int),
	}
	if r.now == nil {
		r.now = func() time.Time { return time.Now().UTC() }
	}
	if r.newID == nil {
		r.newID = randomID
	}
	return r
}

func randomID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "C-" + hex.EncodeToString(b[:])
}

func (r *Router) Send(ctx context.Context, m Message) (Conversation, error) {
	body := strings.TrimSpace(m.Body)
	if body == "" {
		return Conversation{}, model.BadRequest("body is required")
	}
	to := strings.TrimSpace(m.To)
	recipients, err := r.resolve(ctx, to)
	if err != nil {
		return Conversation{}, err
	}

	convID := r.newID()
	now := r.now()
	conv := Conversation{ID: convID, To: to, CreatedAt: now}
	var sendErr error
	for _, id := range recipients {
		msg, err := r.threads.Append(ctx, id, convID, model.Message{
			ThreadKind: model.ThreadAgent,
			ThreadID:   id,
			AuthorID:   model.Owner.ID,
			AuthorRole: model.RoleOwner,
			Kind:       model.MsgMessage,
			Body:       body,
			CreatedAt:  now,
		})
		if err != nil {
			sendErr = err
			break
		}
		conv.Recipients = append(conv.Recipients, id)
		conv.Messages = append(conv.Messages, msg)
	}

	if len(conv.Recipients) > 0 {
		r.mu.Lock()
		r.convs[convID] = Conversation{ID: convID, To: to, Recipients: conv.Recipients, CreatedAt: now}
		r.order = append(r.order, convID)
		for _, id := range conv.Recipients {
			r.unread[id]++
		}
		r.mu.Unlock()
	}
	if sendErr != nil {
		return Conversation{}, sendErr
	}
	return conv, nil
}

func (r *Router) resolve(ctx context.Context, to string) ([]string, error) {
	kind, id, _ := strings.Cut(to, ":")
	var ids []string
	var err error
	switch {
	case kind == targetLeader && id == "":
		var leader string
		leader, err = r.dir.Leader(ctx)
		if leader != "" {
			ids = []string{leader}
		}
	case id == "":
		return nil, model.BadRequest("invalid recipient %q", to)
	case kind == targetAgent:
		var a *model.Agent
		a, err = r.dir.Agent(ctx, id)
		if err == nil {
			if a == nil {
				return nil, model.NotFound("agent %s not found", id)
			}
			if a.Status == model.AgentRevoked {
				return nil, model.Conflict("agent %s is revoked", id)
			}
			ids = []string{a.ID}
		}
	case kind == targetComponent:
		ids, err = r.dir.ByComponent(ctx, id)
	case kind == targetBrief:
		ids, err = r.dir.ByBrief(ctx, id)
	default:
		return nil, model.BadRequest("invalid recipient %q", to)
	}
	if err != nil {
		return nil, err
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) == 0 {
		return nil, model.NotFound("no agents for %s", to)
	}
	return ids, nil
}

func (r *Router) Unread(agentID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.unread[agentID]
}

func (r *Router) MarkRead(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.unread, agentID)
}

func (r *Router) Conversations() []Conversation {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Conversation, 0, len(r.order))
	for i := len(r.order) - 1; i >= 0; i-- {
		out = append(out, r.convs[r.order[i]])
	}
	return out
}

func (r *Router) Conversation(ctx context.Context, id string) (Conversation, error) {
	r.mu.Lock()
	conv, ok := r.convs[id]
	r.mu.Unlock()
	if !ok {
		return Conversation{}, model.NotFound("conversation %s not found", id)
	}

	for _, agentID := range conv.Recipients {
		entries, err := r.threads.Messages(ctx, agentID, 0)
		if err != nil {
			return Conversation{}, err
		}
		conv.Messages = append(conv.Messages, window(entries, id, agentID)...)
	}
	slices.SortStableFunc(conv.Messages, func(a, b model.Message) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return int(a.ID - b.ID)
	})
	return conv, nil
}

func window(entries []Entry, convID, agentID string) []model.Message {
	var out []model.Message
	open := false
	for _, e := range entries {
		owner := e.Message.AuthorRole == model.RoleOwner
		switch {
		case owner && e.ConvID == convID:
			open = true
			out = append(out, e.Message)
		case owner:
			if open {
				return out
			}
		case open && e.Message.AuthorID == agentID && e.Message.Kind == model.MsgMessage:
			out = append(out, e.Message)
		}
	}
	return out
}

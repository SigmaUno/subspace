package acceptance

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/servertest"
)

const reconnectDelay = 50 * time.Millisecond

type pin struct {
	rt   http.RoundTripper
	node string
}

func (p pin) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(servertest.NodeHeader, p.node)
	return p.rt.RoundTrip(r)
}

func pinnedClient(node string) *http.Client {
	return &http.Client{Timeout: 60 * time.Second, Transport: pin{rt: http.DefaultTransport, node: node}}
}

type sseWatch struct {
	owner    *client.Owner
	topics   []string
	connects atomic.Int64
	events   atomic.Int64
}

func (s *sseWatch) run(ctx context.Context, onEvent func()) {
	lastID := ""
	for ctx.Err() == nil {
		stream, err := s.owner.EventsFrom(ctx, lastID, s.topics...)
		if err == nil {
			s.connects.Add(1)
			for {
				ev, err := stream.Next()
				if err != nil {
					break
				}
				s.events.Add(1)
				if ev.ID != "" {
					lastID = ev.ID
				}
				onEvent()
			}
			_ = stream.Close()
		}
		select {
		case <-ctx.Done():
		case <-time.After(reconnectDelay):
		}
	}
}

type pushWatch struct {
	mcp     *client.MCP
	mu      sync.Mutex
	seen    map[int64]bool
	updates int
	resyncs int
}

func newPushWatch(ctx context.Context, url, token, node, uri string) (*pushWatch, error) {
	m := client.NewMCP(url+"/mcp", token, client.WithClientName("push-watch"), client.WithHTTPClient(pinnedClient(node)))
	if err := m.Initialize(ctx); err != nil {
		return nil, err
	}
	if err := m.Subscribe(ctx, uri); err != nil {
		return nil, err
	}
	p := &pushWatch{mcp: m, seen: map[int64]bool{}}
	ch := m.Notifications(ctx)
	go func() {
		for n := range ch {
			if ev, ok := n.Event(); ok && ev.MessageID != 0 {
				p.mark(ev.MessageID)
			}
			if _, ok := n.ResourceURI(); ok {
				p.mu.Lock()
				p.updates++
				p.mu.Unlock()
			}
			if n.Resync() {
				p.reread(ctx)
				p.mu.Lock()
				p.resyncs++
				p.mu.Unlock()
			}
		}
	}()
	return p, nil
}

func (p *pushWatch) mark(id int64) {
	p.mu.Lock()
	p.seen[id] = true
	p.mu.Unlock()
}

func (p *pushWatch) reread(ctx context.Context) {
	msgs, err := p.mcp.ReadThread(ctx, "agent:me", 0, 200)
	if err != nil {
		return
	}
	for _, m := range msgs {
		p.mark(m.ID)
	}
}

func (p *pushWatch) has(id int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen[id]
}

func (p *pushWatch) updated() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.updates
}

func (p *pushWatch) resynced() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resyncs
}

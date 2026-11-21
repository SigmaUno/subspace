package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
)

const (
	DefaultPushBuffer = 256
	DefaultPushPing   = 15 * time.Second

	MethodToolsChanged     = "notifications/tools/list_changed"
	MethodResourceUpdated  = "notifications/resources/updated"
	MethodSubspaceEvent    = "notifications/subspace/event"
	MethodSubspaceResync   = "notifications/subspace/resync"
	maxSubscriptions       = 100
	resourceTask           = "subspace://task/"
	resourceAgentThread    = "subspace://thread/agent/"
	resourceTaskThread     = "subspace://thread/task/"
	closeReplaced          = "replaced"
	closeSessionEnded      = "session_ended"
	closeShutdown          = "shutdown"
	resyncOverflow         = "overflow"
	resyncUnknownEventID   = "unknown_event_id"
	errNoResource          = -32002
	errInvalidResourceArgs = -32602
)

var (
	errNoSuchResource    = errors.New("resource not found")
	errSubscriptionStore = errors.New("subscriptions are temporarily unavailable")
)

type SubscriptionStore interface {
	AddSubscription(ctx context.Context, sid, uri string) error
	RemoveSubscription(ctx context.Context, sid, uri string) error
	Subscribers(ctx context.Context, uri string) ([]string, error)
}

type TaskOwners interface {
	Owners(ctx context.Context, taskID string) (assignee, reviewer string, ok bool)
}

type Push struct {
	Buffer int
	Ping   time.Duration
	Tasks  TaskOwners
	Subs   SubscriptionStore
	Start  func()

	once    sync.Once
	mu      sync.Mutex
	seq     uint64
	streams map[string]*stream
	byAgent map[string]map[string]*stream
	byURI   map[string]map[string]*stream
}

type entry struct {
	id   int64
	data []byte
}

type conn struct {
	cursor int64
	resync string
	wake   chan struct{}
	done   chan struct{}
	reason string
}

type stream struct {
	id    string
	agent string
	seq   uint64

	mu   sync.Mutex
	buf  []entry
	next int64
	conn *conn
	subs map[string]struct{}
}

func NewPush() *Push {
	return &Push{
		Buffer:  DefaultPushBuffer,
		Ping:    DefaultPushPing,
		streams: map[string]*stream{},
		byAgent: map[string]map[string]*stream{},
		byURI:   map[string]map[string]*stream{},
	}
}

func (p *Push) stream(sid, agent string) *stream {
	p.mu.Lock()
	st, ok := p.streams[sid]
	if !ok {
		for len(p.byAgent[agent]) >= maxSessionsPer {
			p.dropLocked(p.oldestLocked(agent))
		}
		p.seq++
		st = &stream{id: sid, agent: agent, seq: p.seq, buf: make([]entry, max(p.Buffer, 1)), next: 1, subs: map[string]struct{}{}}
		p.streams[sid] = st
		if p.byAgent[agent] == nil {
			p.byAgent[agent] = map[string]*stream{}
		}
		p.byAgent[agent][sid] = st
	}
	p.mu.Unlock()
	if !ok && p.Start != nil {
		p.once.Do(p.Start)
	}
	return st
}

func (p *Push) oldestLocked(agent string) *stream {
	var old *stream
	for _, st := range p.byAgent[agent] {
		if old == nil || st.seq < old.seq {
			old = st
		}
	}
	return old
}

func (p *Push) Drop(sid string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if st, ok := p.streams[sid]; ok {
		p.dropLocked(st)
	}
}

func (p *Push) DropAgent(agent string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, st := range p.byAgent[agent] {
		p.dropLocked(st)
	}
}

func (p *Push) dropLocked(st *stream) {
	sid := st.id
	delete(p.streams, sid)
	delete(p.byAgent[st.agent], sid)
	if len(p.byAgent[st.agent]) == 0 {
		delete(p.byAgent, st.agent)
	}
	st.mu.Lock()
	for uri := range st.subs {
		p.unindexLocked(uri, sid)
	}
	if st.conn != nil {
		st.conn.close(closeSessionEnded)
		st.conn = nil
	}
	st.mu.Unlock()
}

func (p *Push) unindexLocked(uri, sid string) {
	delete(p.byURI[uri], sid)
	if len(p.byURI[uri]) == 0 {
		delete(p.byURI, uri)
	}
}

func (c *conn) close(reason string) {
	c.reason = reason
	close(c.done)
}

func (s *stream) push(data []byte) {
	s.mu.Lock()
	s.buf[s.next%int64(len(s.buf))] = entry{id: s.next, data: data}
	s.next++
	c := s.conn
	s.mu.Unlock()
	if c != nil {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
}

func (s *stream) attach(after int64, resuming bool) *conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &conn{cursor: s.next, wake: make(chan struct{}, 1), done: make(chan struct{})}
	if resuming {
		c.cursor = after + 1
		if c.cursor > s.next {
			c.cursor, c.resync = s.next, resyncUnknownEventID
		}
	}
	if s.conn != nil {
		s.conn.close(closeReplaced)
	}
	s.conn = c
	c.wake <- struct{}{}
	return c
}

func (s *stream) detach(c *conn) {
	s.mu.Lock()
	if s.conn == c {
		s.conn = nil
	}
	s.mu.Unlock()
}

func (s *stream) take(c *conn) (frames []entry, dropped int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	oldest := max(s.next-int64(len(s.buf)), 1)
	if c.cursor < oldest {
		dropped = oldest - c.cursor
		c.cursor = oldest
	}
	for ; c.cursor < s.next; c.cursor++ {
		frames = append(frames, s.buf[c.cursor%int64(len(s.buf))])
	}
	return frames, dropped
}

type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

func encodeNotification(method string, params any) []byte {
	b, err := json.Marshal(notification{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return nil
	}
	return b
}

func (p *Push) agentStreams(agent string) []*stream {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*stream, 0, len(p.byAgent[agent]))
	for _, st := range p.byAgent[agent] {
		out = append(out, st)
	}
	return out
}

func (p *Push) Notify(agent, method string, params any) {
	data := encodeNotification(method, params)
	if data == nil {
		return
	}
	for _, st := range p.agentStreams(agent) {
		st.push(data)
	}
}

func (p *Push) Broadcast(method string, params any) {
	data := encodeNotification(method, params)
	if data == nil {
		return
	}
	p.mu.Lock()
	all := make([]*stream, 0, len(p.streams))
	for _, st := range p.streams {
		all = append(all, st)
	}
	p.mu.Unlock()
	for _, st := range all {
		st.push(data)
	}
}

func (p *Push) Event(agent string, params map[string]any) {
	p.Notify(agent, MethodSubspaceEvent, params)
}

func (p *Push) ToolsChanged(agents ...string) {
	for _, a := range agents {
		p.Notify(a, MethodToolsChanged, nil)
	}
}

func (p *Push) Resync(dropped int) {
	p.Broadcast(MethodSubspaceResync, map[string]any{"reason": resyncOverflow, "dropped": dropped})
}

func (p *Push) owns(ctx context.Context, agent, uri string) bool {
	switch {
	case strings.HasPrefix(uri, resourceAgentThread):
		return strings.TrimPrefix(uri, resourceAgentThread) == agent
	case strings.HasPrefix(uri, resourceTask):
		return p.ownsTask(ctx, agent, strings.TrimPrefix(uri, resourceTask))
	case strings.HasPrefix(uri, resourceTaskThread):
		return p.ownsTask(ctx, agent, strings.TrimPrefix(uri, resourceTaskThread))
	}
	return false
}

func (p *Push) ownsTask(ctx context.Context, agent, taskID string) bool {
	if p.Tasks == nil || taskID == "" {
		return false
	}
	assignee, reviewer, ok := p.Tasks.Owners(ctx, taskID)
	return ok && (assignee == agent || reviewer == agent)
}

func TaskResource(id string) string { return resourceTask + id }

func ThreadResource(kind, id string) string { return "subspace://thread/" + kind + "/" + id }

func (p *Push) Subscribe(ctx context.Context, sid, agent, uri string) error {
	if !p.owns(ctx, agent, uri) {
		return errNoSuchResource
	}
	if err := p.subscribeLocal(sid, agent, uri); err != nil {
		return err
	}
	if p.Subs != nil {
		if err := p.Subs.AddSubscription(ctx, sid, uri); err != nil {
			p.unsubscribeLocal(sid, uri)
			return errSubscriptionStore
		}
	}
	return nil
}

func (p *Push) subscribeLocal(sid, agent, uri string) error {
	st := p.stream(sid, agent)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.streams[sid] != st {
		return errNoSuchResource
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.subs[uri]; ok {
		return nil
	}
	if len(st.subs) >= maxSubscriptions {
		return errors.New("too many subscriptions")
	}
	st.subs[uri] = struct{}{}
	if p.byURI[uri] == nil {
		p.byURI[uri] = map[string]*stream{}
	}
	p.byURI[uri][sid] = st
	return nil
}

func (p *Push) Unsubscribe(ctx context.Context, sid, uri string) {
	p.unsubscribeLocal(sid, uri)
	if p.Subs != nil {
		_ = p.Subs.RemoveSubscription(ctx, sid, uri)
	}
}

func (p *Push) unsubscribeLocal(sid, uri string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st, ok := p.streams[sid]
	if !ok {
		return
	}
	st.mu.Lock()
	delete(st.subs, uri)
	st.mu.Unlock()
	p.unindexLocked(uri, sid)
}

func (p *Push) subscribers(ctx context.Context, uri string) []*stream {
	p.mu.Lock()
	subs := make([]*stream, 0, len(p.byURI[uri]))
	for _, st := range p.byURI[uri] {
		subs = append(subs, st)
	}
	attached := len(p.streams) > 0
	p.mu.Unlock()
	if p.Subs == nil || !attached {
		return subs
	}
	ids, err := p.Subs.Subscribers(ctx, uri)
	if err != nil {
		return subs
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range ids {
		if st, ok := p.streams[id]; ok && !slices.Contains(subs, st) {
			subs = append(subs, st)
		}
	}
	return subs
}

func (p *Push) Updated(ctx context.Context, uri string) {
	subs := p.subscribers(ctx, uri)
	if len(subs) == 0 {
		return
	}
	data := encodeNotification(MethodResourceUpdated, map[string]string{"uri": uri})
	for _, st := range subs {
		if p.owns(ctx, st.agent, uri) {
			st.push(data)
		}
	}
}

func lastEventID(r *http.Request) (int64, bool, error) {
	v := r.Header.Get("Last-Event-ID")
	if v == "" {
		return 0, false, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, false, errors.New("invalid Last-Event-ID")
	}
	return n, true, nil
}

func acceptsEventStream(r *http.Request) bool {
	for _, h := range r.Header.Values("Accept") {
		for _, part := range strings.Split(h, ",") {
			mt, _, _ := strings.Cut(strings.TrimSpace(part), ";")
			if strings.EqualFold(strings.TrimSpace(mt), "text/event-stream") {
				return true
			}
		}
	}
	return false
}

func appendFrame(b *bytes.Buffer, id int64, data []byte) {
	if id > 0 {
		b.WriteString("id: ")
		b.WriteString(strconv.FormatInt(id, 10))
		b.WriteByte('\n')
	}
	b.WriteString("event: message\ndata: ")
	b.Write(data)
	b.WriteString("\n\n")
}

func appendClose(b *bytes.Buffer, reason string) {
	b.WriteString("event: close\ndata: ")
	body := map[string]any{"reason": reason}
	if reason == closeReplaced {
		body["status"] = http.StatusConflict
	}
	j, _ := json.Marshal(body)
	b.Write(j)
	b.WriteString("\n\n")
}

func (p *Push) Serve(w http.ResponseWriter, r *http.Request, sid, agent string) {
	after, resuming, err := lastEventID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	st := p.stream(sid, agent)
	c := st.attach(after, resuming)
	defer st.detach(c)

	ping := p.Ping
	if ping <= 0 {
		ping = DefaultPushPing
	}
	stall := max(2*ping, time.Second)
	rc := http.NewResponseController(w)
	arm := func() { _ = rc.SetWriteDeadline(time.Now().Add(stall)) }
	shutdown := events.ShutdownSignal(r.Context())

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	arm()
	if rc.Flush() != nil {
		return
	}

	t := time.NewTicker(ping)
	defer t.Stop()
	var out bytes.Buffer
	send := func() bool {
		arm()
		_, err := w.Write(out.Bytes())
		out.Reset()
		return err == nil && rc.Flush() == nil
	}
	for {
		frames, dropped := st.take(c)
		if c.resync == "" && dropped > 0 {
			c.resync = resyncOverflow
		}
		if c.resync != "" {
			appendFrame(&out, 0, encodeNotification(MethodSubspaceResync, map[string]any{"reason": c.resync, "dropped": dropped}))
			c.resync = ""
		}
		for _, f := range frames {
			appendFrame(&out, f.id, f.data)
		}
		if out.Len() > 0 && !send() {
			return
		}
		select {
		case <-c.wake:
		case <-t.C:
			out.WriteString(": ping\n\n")
			if !send() {
				return
			}
		case <-c.done:
			appendClose(&out, c.reason)
			send()
			return
		case <-shutdown:
			appendClose(&out, closeShutdown)
			send()
			return
		case <-r.Context().Done():
			return
		}
	}
}

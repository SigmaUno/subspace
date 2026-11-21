package agentout

import (
	"container/list"
	"sync"
	"time"
)

const lineOverhead = 96

type Line struct {
	Seq     int64     `json:"seq"`
	AgentID string    `json:"agent_id"`
	TaskID  *string   `json:"task_id"`
	Stream  string    `json:"stream"`
	Text    string    `json:"text"`
	TS      time.Time `json:"ts"`
}

func (l *Line) cost() int {
	n := len(l.Text) + lineOverhead
	if l.TaskID != nil {
		n += len(*l.TaskID)
	}
	return n
}

type Limits struct {
	Lines       int
	Bytes       int
	GlobalBytes int
}

type ring struct {
	agent     string
	lines     []Line
	start     int
	bytes     int
	seq       int64
	persisted int64
	clientSeq int64
	elem      *list.Element
}

func (r *ring) len() int { return len(r.lines) - r.start }

func (r *ring) at(i int) *Line { return &r.lines[r.start+i] }

func (r *ring) popFront() int {
	c := r.lines[r.start].cost()
	r.lines[r.start] = Line{}
	r.start++
	r.bytes -= c
	if n := r.len(); n == 0 {
		r.lines, r.start = nil, 0
	} else if r.start >= 64 && r.start >= n {
		r.lines = append(make([]Line, 0, n*2), r.lines[r.start:]...)
		r.start = 0
	}
	return c
}

type Buffer struct {
	mu     sync.Mutex
	lim    Limits
	agents map[string]*ring
	lru    *list.List
	bytes  int
}

func NewBuffer(lim Limits) *Buffer {
	return &Buffer{lim: lim, agents: map[string]*ring{}, lru: list.New()}
}

func (b *Buffer) ring(agent string) *ring {
	r := b.agents[agent]
	if r == nil {
		r = &ring{agent: agent, clientSeq: -1}
		b.agents[agent] = r
	}
	return r
}

func (b *Buffer) Append(agent string, taskID *string, stream string, texts []string, ts time.Time) []Line {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.append(agent, taskID, stream, texts, ts)
}

func (b *Buffer) append(agent string, taskID *string, stream string, texts []string, ts time.Time) []Line {
	r := b.ring(agent)
	out := make([]Line, 0, len(texts))
	for _, t := range texts {
		r.seq++
		l := Line{Seq: r.seq, AgentID: agent, TaskID: taskID, Stream: stream, Text: t, TS: ts}
		r.lines = append(r.lines, l)
		c := l.cost()
		r.bytes += c
		b.bytes += c
		out = append(out, l)
	}
	if len(texts) > 0 {
		b.touch(r)
	}
	for r.len() > 0 && (r.len() > b.lim.Lines || r.bytes > b.lim.Bytes) {
		b.bytes -= r.popFront()
	}
	if r.len() == 0 && r.elem != nil {
		b.lru.Remove(r.elem)
		r.elem = nil
	}
	b.enforceGlobal()
	return out
}

func (b *Buffer) touch(r *ring) {
	if r.elem == nil {
		r.elem = b.lru.PushFront(r)
		return
	}
	b.lru.MoveToFront(r.elem)
}

func (b *Buffer) enforceGlobal() {
	for b.bytes > b.lim.GlobalBytes {
		e := b.lru.Back()
		if e == nil {
			return
		}
		r := e.Value.(*ring)
		b.bytes -= r.popFront()
		if r.len() == 0 {
			b.lru.Remove(e)
			r.elem = nil
		}
	}
}

func (b *Buffer) ClientSeq(agent string, seq int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.ring(agent)
	if seq <= r.clientSeq {
		return false
	}
	r.clientSeq = seq
	return true
}

func (b *Buffer) LastSeq(agent string) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r := b.agents[agent]; r != nil {
		return r.seq
	}
	return 0
}

type Page struct {
	Lines []Line
	First int64
	Last  int64
	More  bool
}

func (b *Buffer) Since(agent string, since int64, limit int, taskID string) Page {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.agents[agent]
	if r == nil || r.len() == 0 {
		p := Page{Lines: []Line{}}
		if r != nil {
			p.Last, p.First = r.seq, r.seq+1
		}
		return p
	}
	p := Page{First: r.at(0).Seq, Last: r.seq}
	match := func(l *Line) bool {
		return l.Seq > since && (taskID == "" || (l.TaskID != nil && *l.TaskID == taskID))
	}
	var picked []Line
	if since > 0 {
		for i := 0; i < r.len(); i++ {
			if l := r.at(i); match(l) {
				if len(picked) == limit {
					p.More = true
					break
				}
				picked = append(picked, *l)
			}
		}
	} else {
		for i := r.len() - 1; i >= 0; i-- {
			if l := r.at(i); match(l) {
				if len(picked) == limit {
					p.More = true
					break
				}
				picked = append(picked, *l)
			}
		}
		for i, j := 0, len(picked)-1; i < j; i, j = i+1, j-1 {
			picked[i], picked[j] = picked[j], picked[i]
		}
	}
	if picked == nil {
		picked = []Line{}
	}
	p.Lines = picked
	return p
}

type Tail struct {
	Line  Line
	Lines int
}

func (b *Buffer) Latest() map[string]Tail {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]Tail, b.lru.Len())
	for e := b.lru.Front(); e != nil; e = e.Next() {
		r := e.Value.(*ring)
		out[r.agent] = Tail{Line: *r.at(r.len() - 1), Lines: r.len()}
	}
	return out
}

func (b *Buffer) Usage() (bytes, agents int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bytes, b.lru.Len()
}

type batch struct {
	agent string
	lines []Line
	seq   int64
}

func (b *Buffer) dirty(keep int) []batch {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []batch
	for _, r := range b.agents {
		if r.seq <= r.persisted {
			continue
		}
		bt := batch{agent: r.agent, seq: r.seq}
		for i := max(r.len()-keep, 0); i < r.len(); i++ {
			if l := r.at(i); l.Seq > r.persisted {
				bt.lines = append(bt.lines, *l)
			}
		}
		out = append(out, bt)
	}
	return out
}

func (b *Buffer) markPersisted(agent string, seq int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r := b.agents[agent]; r != nil && seq > r.persisted {
		r.persisted = seq
	}
}

func (b *Buffer) restore(lines []Line) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, l := range lines {
		r := b.ring(l.AgentID)
		if l.Seq <= r.seq {
			continue
		}
		r.seq = l.Seq - 1
		b.append(l.AgentID, l.TaskID, l.Stream, []string{l.Text}, l.TS)
		r.persisted = r.seq
	}
}

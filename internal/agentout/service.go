package agentout

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/ratelimit"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	MaxChunk     = 4096
	EventType    = "agent.output"
	TopicAll     = "output"
	TopicPrefix  = "output:"
	DefaultLimit = 500
	MaxLimit     = 2000
	replay       = 1000
)

var streams = map[string]bool{"stdout": true, "stderr": true, "note": true}

type Directory interface {
	Agent(ctx context.Context, id string) error
	Active(ctx context.Context) ([]string, error)
	CanSeeTask(ctx context.Context, a model.Actor, taskID string) error
}

type Config struct {
	Limits     Limits
	Rate       ratelimit.Limit
	Persist    int
	Scan       func(ctx context.Context, a model.Actor, text string) string
	FlushEvery time.Duration
	Now        func() time.Time
	Log        *slog.Logger
}

func DefaultConfig() Config {
	return Config{
		Limits:     Limits{Lines: 2000, Bytes: 512 << 10, GlobalBytes: 64 << 20},
		Rate:       ratelimit.Limit{PerMinute: 600, Burst: 60},
		Persist:    200,
		FlushEvery: 2 * time.Second,
	}
}

type Service struct {
	cfg  Config
	buf  *Buffer
	hub  events.Hub
	dir  Directory
	st   store.Store
	rate *ratelimit.Limiter
}

func New(st store.Store, dir Directory, cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	hub := events.NewHub()
	if h, ok := hub.(interface{ SetReplay(int) }); ok {
		h.SetReplay(replay)
	}
	return &Service{
		cfg: cfg, buf: NewBuffer(cfg.Limits), hub: hub, dir: dir, st: st,
		rate: ratelimit.New(ratelimit.Options{Default: cfg.Rate}),
	}
}

func (s *Service) Buffer() *Buffer { return s.buf }

func (s *Service) Hub() events.Hub { return s.hub }

func (s *Service) Run(ctx context.Context) {
	if s.cfg.Persist <= 0 || s.st == nil {
		return
	}
	every := s.cfg.FlushEvery
	if every <= 0 {
		every = 2 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.flush(ctx)
		}
	}
}

func (s *Service) Restore(ctx context.Context) error {
	if s.cfg.Persist <= 0 || s.st == nil {
		return nil
	}
	lines, err := load(ctx, s.st)
	if err != nil {
		return err
	}
	s.buf.restore(lines)
	return nil
}

func (s *Service) flush(ctx context.Context) {
	batches := s.buf.dirty(s.cfg.Persist)
	if len(batches) == 0 {
		return
	}
	if err := persist(ctx, s.st, batches, s.cfg.Persist); err != nil {
		if ctx.Err() == nil {
			s.cfg.Log.Warn("agent output: persisting tail failed", "err", err)
		}
		return
	}
	for _, bt := range batches {
		s.buf.markPersisted(bt.agent, bt.seq)
	}
}

type Input struct {
	TaskID string `json:"task_id"`
	Stream string `json:"stream"`
	Text   string `json:"text"`
	Seq    *int64 `json:"seq"`
}

type Result struct {
	OK        bool  `json:"ok"`
	Seq       int64 `json:"seq"`
	Lines     int   `json:"lines"`
	Truncated bool  `json:"truncated"`
	Duplicate bool  `json:"duplicate"`
}

type Chunk struct {
	AgentID string  `json:"agent_id"`
	TaskID  *string `json:"task_id"`
	Lines   []Line  `json:"lines"`
}

func (s *Service) Append(ctx context.Context, a model.Actor, in Input) (*Result, error) {
	if !streams[in.Stream] {
		return nil, model.BadRequest("stream must be stdout, stderr or note")
	}
	if in.Text == "" {
		return nil, model.BadRequest("text is required")
	}
	if in.Seq != nil && *in.Seq < 0 {
		return nil, model.BadRequest("seq must not be negative")
	}
	if !s.rate.Allow(a.ID) {
		return nil, model.Errf("rate_limited", "log_output rate limit exceeded; send larger chunks less often")
	}
	var task *string
	if in.TaskID != "" {
		if err := s.dir.CanSeeTask(ctx, a, in.TaskID); err != nil {
			return nil, err
		}
		id := in.TaskID
		task = &id
	}
	if in.Seq != nil && !s.buf.ClientSeq(a.ID, *in.Seq) {
		return &Result{OK: true, Seq: s.buf.LastSeq(a.ID), Duplicate: true}, nil
	}
	texts, truncated := s.clean(ctx, a, in.Text)
	if len(texts) == 0 {
		return &Result{OK: true, Seq: s.buf.LastSeq(a.ID), Truncated: truncated}, nil
	}
	lines := s.buf.Append(a.ID, task, in.Stream, texts, s.cfg.Now().UTC())
	s.hub.Publish([]string{TopicAll, TopicPrefix + a.ID}, EventType, Chunk{AgentID: a.ID, TaskID: task, Lines: lines})
	return &Result{OK: true, Seq: lines[len(lines)-1].Seq, Lines: len(lines), Truncated: truncated}, nil
}

func (s *Service) clean(ctx context.Context, a model.Actor, text string) ([]string, bool) {
	if s.cfg.Scan == nil || len(text) == 0 {
		return Clean(text, MaxChunk)
	}
	head := text[:min(len(text), MaxChunk)]
	lines, _ := Clean(s.cfg.Scan(ctx, a, head), MaxChunk)
	return lines, len(text) > MaxChunk
}

type Output struct {
	AgentID   string `json:"agent_id"`
	Lines     []Line `json:"lines"`
	FirstSeq  int64  `json:"first_seq"`
	LastSeq   int64  `json:"last_seq"`
	NextSince int64  `json:"next_since"`
	More      bool   `json:"more"`
	Gap       bool   `json:"gap"`
}

func (s *Service) Since(ctx context.Context, agent string, since int64, limit int, taskID string) (*Output, error) {
	if err := s.dir.Agent(ctx, agent); err != nil {
		return nil, err
	}
	since = max(since, 0)
	reset := since > s.buf.LastSeq(agent)
	if reset {
		since = 0
	}
	p := s.buf.Since(agent, since, limit, taskID)
	out := &Output{AgentID: agent, Lines: p.Lines, FirstSeq: p.First, LastSeq: p.Last, More: p.More, NextSince: since}
	if n := len(p.Lines); n > 0 {
		out.NextSince = p.Lines[n-1].Seq
	} else if since == 0 {
		out.NextSince = p.Last
	}
	out.Gap = reset || (since > 0 && since < p.First-1)
	return out, nil
}

type Latest struct {
	AgentID   string    `json:"agent_id"`
	Line      Line      `json:"line"`
	Lines     int       `json:"lines"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Service) Latest(ctx context.Context) ([]Latest, error) {
	active, err := s.dir.Active(ctx)
	if err != nil {
		return nil, err
	}
	tails := s.buf.Latest()
	out := []Latest{}
	for _, id := range active {
		if t, ok := tails[id]; ok {
			out = append(out, Latest{AgentID: id, Line: t.Line, Lines: t.Lines, UpdatedAt: t.Line.TS})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].AgentID < out[j].AgentID
	})
	return out, nil
}

func SplitTopics(topics []string) (output bool, mixed bool) {
	var other bool
	for _, t := range topics {
		if t == TopicAll || strings.HasPrefix(t, TopicPrefix) {
			output = true
		} else {
			other = true
		}
	}
	return output, output && other
}

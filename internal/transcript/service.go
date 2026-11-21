package transcript

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/agentout"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/redact"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	queuedOverhead = 64
	maskedLine     = "****"
	keyBegin       = "-----BEGIN "
	keyEnd         = "-----END "
	keyMarker      = "PRIVATE KEY-----"
)

var flushStatuses = map[string]bool{
	model.StatusSubmitted: true, model.StatusBlocked: true, model.StatusCancelled: true, model.StatusDone: true,
}

type Info struct {
	BriefID string
	Status  string
}

type Tasks interface {
	Task(ctx context.Context, taskID string) (Info, error)
}

type Config struct {
	Dir          string
	MaxBytes     int64
	SegmentLines int
	SegmentBytes int
	PendingBytes int
	FlushEvery   time.Duration
	CheckEvery   time.Duration
	Now          func() time.Time
	Log          *slog.Logger
}

func DefaultConfig() Config {
	return Config{MaxBytes: 256 << 20, SegmentLines: 5000, SegmentBytes: 256 << 10, PendingBytes: 1 << 20, FlushEvery: 5 * time.Minute, CheckEvery: 2 * time.Second}
}

type queued struct {
	ts     time.Time
	agent  string
	stream string
	text   string
	clean  bool
}

type Service struct {
	cfg   Config
	st    store.Store
	tasks Tasks
	wake  chan struct{}

	mu    sync.Mutex
	queue map[string][]queued
	size  map[string]int

	flushMu sync.Mutex
	heads   map[string]int64
	briefs  map[string]string
	inKey   map[string]bool
}

func New(st store.Store, tasks Tasks, cfg Config) (*Service, error) {
	if cfg.Dir == "" {
		return nil, errors.New("transcript: directory is required")
	}
	d := DefaultConfig()
	if cfg.SegmentLines <= 0 {
		cfg.SegmentLines = d.SegmentLines
	}
	if cfg.SegmentBytes <= 0 {
		cfg.SegmentBytes = d.SegmentBytes
	}
	if cfg.PendingBytes <= 0 {
		cfg.PendingBytes = d.PendingBytes
	}
	if cfg.FlushEvery <= 0 {
		cfg.FlushEvery = d.FlushEvery
	}
	if cfg.CheckEvery <= 0 {
		cfg.CheckEvery = d.CheckEvery
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if err := os.MkdirAll(cfg.Dir, 0o750); err != nil {
		return nil, err
	}
	return &Service{
		cfg: cfg, st: st, tasks: tasks, wake: make(chan struct{}, 1),
		queue: map[string][]queued{}, size: map[string]int{},
		heads: map[string]int64{}, briefs: map[string]string{}, inKey: map[string]bool{},
	}, nil
}

func (s *Service) Dir() string { return s.cfg.Dir }

func (s *Service) Add(lines []agentout.Line) {
	s.mu.Lock()
	full := false
	for _, l := range lines {
		if l.TaskID == nil || !taskIDRe.MatchString(*l.TaskID) {
			continue
		}
		id := *l.TaskID
		s.queue[id] = append(s.queue[id], queued{ts: l.TS, agent: l.AgentID, stream: l.Stream, text: l.Text})
		s.size[id] += len(l.Text) + queuedOverhead
		full = full || s.size[id] >= s.cfg.PendingBytes
	}
	s.mu.Unlock()
	if full {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

func (s *Service) pendingTasks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.queue))
	for id := range s.queue {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (s *Service) settle(ctx context.Context) {
	for _, id := range s.pendingTasks() {
		info, err := s.tasks.Task(ctx, id)
		if err != nil || !flushStatuses[info.Status] {
			continue
		}
		if err := s.Flush(ctx, id); err != nil && ctx.Err() == nil {
			s.cfg.Log.Warn("transcript: flush of a settled task failed", "task", id, "err", err)
		}
	}
}

func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(s.cfg.FlushEvery)
	defer t.Stop()
	c := time.NewTicker(s.cfg.CheckEvery)
	defer c.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.C:
			s.settle(ctx)
			continue
		case <-t.C:
		case <-s.wake:
		}
		if err := s.Flush(ctx, ""); err != nil && ctx.Err() == nil {
			s.cfg.Log.Warn("transcript: flush failed", "err", err)
		}
	}
}

func (s *Service) take(task string) map[string][]queued {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]queued{}
	for id, q := range s.queue {
		if task == "" || id == task {
			out[id] = q
			delete(s.queue, id)
			delete(s.size, id)
		}
	}
	return out
}

func (s *Service) requeue(task string, rest []queued) {
	if len(rest) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue[task] = append(rest, s.queue[task]...)
	for _, q := range rest {
		s.size[task] += len(q.text) + queuedOverhead
	}
}

func (s *Service) Flush(ctx context.Context, task string) error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	var errs []error
	batches := s.take(task)
	ids := make([]string, 0, len(batches))
	for id := range batches {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := s.flushTask(ctx, id, batches[id]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) flushTask(ctx context.Context, task string, q []queued) error {
	brief, err := s.brief(ctx, task)
	if err != nil {
		s.requeue(task, q)
		return err
	}
	next, ok := s.heads[task]
	if !ok {
		n, err := loadHead(ctx, s.st, task)
		if err != nil {
			s.requeue(task, q)
			return err
		}
		next = max(n, 1)
	}
	for i := range q {
		if !q[i].clean {
			q[i].text, q[i].clean = s.scrub(task, q[i].text), true
		}
	}
	touched := false
	for len(q) > 0 {
		n, raw := 0, 0
		for n < len(q) && n < s.cfg.SegmentLines && raw < s.cfg.SegmentBytes {
			raw += len(q[n].text)
			n++
		}
		entries := make([]Entry, n)
		for i, l := range q[:n] {
			entries[i] = Entry{Offset: next + int64(i), TS: l.ts.UTC(), AgentID: l.agent, Stream: l.stream, Text: l.text}
		}
		seg, err := s.persist(ctx, task, brief, entries)
		if err != nil {
			s.requeue(task, q)
			return err
		}
		next = seg.LastOffset + 1
		s.heads[task] = next
		q = q[n:]
		touched = true
	}
	if touched {
		if err := s.enforce(ctx, brief); err != nil {
			s.cfg.Log.Warn("transcript: enforcing the size limit failed", "brief", brief, "err", err)
		}
	}
	return nil
}

func (s *Service) persist(ctx context.Context, task, brief string, entries []Entry) (*Segment, error) {
	raw, gz, sum, err := encode(entries)
	if err != nil {
		return nil, err
	}
	first, last := entries[0], entries[len(entries)-1]
	seg := &Segment{
		TaskID: task, BriefID: brief, FirstOffset: first.Offset, LastOffset: last.Offset, Lines: len(entries),
		RawBytes: raw, Bytes: int64(len(gz)), SHA256: sum, File: segmentName(task, first.Offset),
		FirstTS: first.TS, LastTS: last.TS, CreatedAt: s.cfg.Now().UTC(),
	}
	if err := writeFile(s.cfg.Dir, seg.File, gz); err != nil {
		return nil, err
	}
	if err := insertSegment(ctx, s.st, seg); err != nil {
		return nil, err
	}
	return seg, nil
}

func (s *Service) brief(ctx context.Context, task string) (string, error) {
	if b, ok := s.briefs[task]; ok {
		return b, nil
	}
	info, err := s.tasks.Task(ctx, task)
	if err != nil {
		return "", err
	}
	s.briefs[task] = info.BriefID
	return info.BriefID, nil
}

func (s *Service) scrub(task, text string) string {
	if s.inKey[task] {
		if strings.Contains(text, keyEnd) && strings.Contains(text, keyMarker) {
			s.inKey[task] = false
		}
		return maskedLine
	}
	out := redact.String(text)
	if i := strings.Index(out, keyBegin); i >= 0 && strings.Contains(out[i:], keyMarker) && !strings.Contains(out[i:], keyEnd) {
		s.inKey[task] = true
	}
	return out
}

func (s *Service) enforce(ctx context.Context, brief string) error {
	if s.cfg.MaxBytes <= 0 {
		return nil
	}
	segs, err := briefSegments(ctx, s.st, brief)
	if err != nil {
		return err
	}
	var total int64
	for _, sg := range segs {
		total += sg.Bytes
	}
	var drop []Segment
	for i := 0; total > s.cfg.MaxBytes && i < len(segs)-1; i++ {
		drop = append(drop, segs[i])
		total -= segs[i].Bytes
	}
	if len(drop) == 0 {
		return nil
	}
	if err := deleteSegments(ctx, s.st, drop, "size_limit", s.cfg.Now().UTC()); err != nil {
		return err
	}
	for _, sg := range drop {
		if err := os.Remove(filepath.Join(s.cfg.Dir, filepath.FromSlash(sg.File))); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.cfg.Log.Warn("transcript: removing a pruned segment failed", "file", sg.File, "err", err)
		}
	}
	return nil
}

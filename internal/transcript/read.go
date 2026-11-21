package transcript

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	DefaultLimit = 500
	MaxLimit     = 2000
	MaxQuery     = 200
	scanCap      = 200000
)

type ReadOptions struct {
	Since int64
	Limit int
	Query string
	At    time.Time
}

type Page struct {
	TaskID      string  `json:"task_id"`
	Lines       []Entry `json:"lines"`
	FirstOffset int64   `json:"first_offset"`
	LastOffset  int64   `json:"last_offset"`
	NextSince   int64   `json:"next_since"`
	More        bool    `json:"more"`
	Gap         bool    `json:"gap"`
	Segments    int     `json:"segments"`
	Bytes       int64   `json:"bytes"`
}

func (s *Service) flushForRead(ctx context.Context, task string) {
	if err := s.Flush(ctx, task); err != nil && ctx.Err() == nil {
		s.cfg.Log.Warn("transcript: flush before read failed", "task", task, "err", err)
	}
}

func (s *Service) Read(ctx context.Context, task string, o ReadOptions) (*Page, error) {
	if _, err := s.tasks.Task(ctx, task); err != nil {
		return nil, err
	}
	switch {
	case o.Since < 0:
		return nil, model.BadRequest("since must be a non-negative integer")
	case o.Limit < 0:
		return nil, model.BadRequest("limit must be a positive integer")
	case len(o.Query) > MaxQuery:
		return nil, model.BadRequest("q must be at most %d bytes", MaxQuery)
	}
	limit := o.Limit
	if limit == 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	s.flushForRead(ctx, task)
	stats, err := taskStats(ctx, s.st, task)
	if err != nil {
		return nil, err
	}
	p := &Page{TaskID: task, Lines: []Entry{}, FirstOffset: stats.FirstOffset, LastOffset: stats.LastOffset,
		NextSince: o.Since, Segments: stats.Segments, Bytes: stats.Bytes}
	p.Gap = stats.Segments > 0 && o.Since+1 < stats.FirstOffset
	segs, err := taskSegments(ctx, s.st, task, o.Since)
	if err != nil {
		return nil, err
	}
	query := strings.ToLower(o.Query)
	scanned := 0
	for _, seg := range segs {
		if !o.At.IsZero() && seg.LastTS.Before(o.At) {
			p.NextSince = seg.LastOffset
			continue
		}
		raw, err := readFile(s.cfg.Dir, seg)
		if err != nil {
			return nil, err
		}
		entries, err := decode(raw, seg)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Offset <= o.Since {
				continue
			}
			if len(p.Lines) == limit || scanned >= scanCap {
				p.More = true
				return p, nil
			}
			scanned++
			p.NextSince = e.Offset
			if !o.At.IsZero() && e.TS.Before(o.At) {
				continue
			}
			if query != "" && !strings.Contains(strings.ToLower(e.Text), query) {
				continue
			}
			p.Lines = append(p.Lines, e)
		}
	}
	return p, nil
}

func (s *Service) Download(ctx context.Context, task string, begin func(size int64) io.Writer) error {
	if _, err := s.tasks.Task(ctx, task); err != nil {
		return err
	}
	s.flushForRead(ctx, task)
	segs, err := taskSegments(ctx, s.st, task, 0)
	if err != nil {
		return err
	}
	var size int64
	for _, seg := range segs {
		size += seg.Bytes
	}
	var out io.Writer
	for _, seg := range segs {
		raw, err := readFile(s.cfg.Dir, seg)
		if err != nil {
			return err
		}
		if out == nil {
			out = begin(size)
		}
		if _, err := out.Write(raw); err != nil {
			return err
		}
	}
	if out == nil {
		begin(0)
	}
	return nil
}

func (s *Service) Stats(ctx context.Context, tasks []string) (map[string]Stats, error) {
	out := make(map[string]Stats, len(tasks))
	for _, id := range tasks {
		st, err := taskStats(ctx, s.st, id)
		if err != nil {
			return nil, err
		}
		if st.Segments > 0 {
			out[id] = st
		}
	}
	return out, nil
}

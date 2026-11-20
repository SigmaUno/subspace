package usage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"strings"
	"time"
)

type Totaler interface {
	SessionTotal(ctx context.Context, source, session string) (Record, error)
}

type Collector interface {
	Name() string
	Collect(ctx context.Context, cursor string, prev Totaler) ([]Record, string, error)
}

func (s *Service) Collect(ctx context.Context, c Collector) (int, error) {
	cur, err := s.Store.Cursor(ctx, c.Name())
	if err != nil {
		return 0, err
	}
	recs, next, err := c.Collect(ctx, cur, s.Store)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", c.Name(), err)
	}
	if len(recs) > 0 {
		if err := s.Record(ctx, recs...); err != nil {
			return 0, err
		}
	}
	return len(recs), s.Store.SetCursor(ctx, c.Name(), next)
}

func (s *Service) Run(ctx context.Context, every time.Duration, cs ...Collector) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		for _, c := range cs {
			if _, err := s.Collect(ctx, c); err != nil && ctx.Err() == nil {
				slog.Warn("usage collector failed", "collector", c.Name(), "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func walkJSONL(ctx context.Context, root string, fn func(fsys fs.FS, name string) error) error {
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	fsys := os.DirFS(root)
	return fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() || path.Ext(name) != ".jsonl" {
			return nil
		}
		return fn(fsys, name)
	})
}

func tail(fsys fs.FS, name string, off int64, fn func(line []byte)) (int64, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return off, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return off, err
	}
	if st.Size() < off {
		off = 0
	}
	if st.Size() == off {
		return off, nil
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		return off, fmt.Errorf("%s: not seekable", name)
	}
	if _, err := rs.Seek(off, io.SeekStart); err != nil {
		return off, err
	}
	br := bufio.NewReaderSize(rs, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return off, nil
			}
			return off, err
		}
		off += int64(len(line))
		fn(line)
	}
}

func loadCursor(cursor string, v any) error {
	if cursor == "" {
		return nil
	}
	return json.Unmarshal([]byte(cursor), v)
}

func saveCursor(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

type ClaudeCode struct {
	Root    string
	AgentID string
}

func (c *ClaudeCode) Name() string { return "claude:" + c.Root }

type claudeLine struct {
	Type      string    `json:"type"`
	SessionID string    `json:"sessionId"`
	Timestamp time.Time `json:"timestamp"`
	RequestID string    `json:"requestId"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens   int64 `json:"input_tokens"`
			OutputTokens  int64 `json:"output_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

func (c *ClaudeCode) Collect(ctx context.Context, cursor string, _ Totaler) ([]Record, string, error) {
	offs := map[string]int64{}
	if err := loadCursor(cursor, &offs); err != nil {
		return nil, cursor, err
	}
	var out []Record
	seen := map[string]int{}
	err := walkJSONL(ctx, c.Root, func(fsys fs.FS, name string) error {
		off, err := tail(fsys, name, offs[name], func(line []byte) {
			var l claudeLine
			if json.Unmarshal(line, &l) != nil || l.Type != "assistant" || l.Message.Usage == nil || l.Message.ID == "" {
				return
			}
			if strings.HasPrefix(l.Message.Model, "<") {
				return
			}
			u := l.Message.Usage
			r := Record{
				TS: l.Timestamp, AgentID: c.AgentID, Provider: "anthropic", Model: l.Message.Model,
				TokensIn: u.InputTokens, TokensOut: u.OutputTokens, CacheRead: u.CacheRead, CacheWrite: u.CacheCreation,
				Source: SourceClaude, Session: l.SessionID, Key: "claude:" + l.Message.ID + ":" + l.RequestID,
			}
			if i, ok := seen[r.Key]; ok {
				out[i] = r
				return
			}
			seen[r.Key] = len(out)
			out = append(out, r)
		})
		offs[name] = off
		return err
	})
	if err != nil {
		return nil, cursor, err
	}
	next, err := saveCursor(offs)
	return out, next, err
}

type Codex struct {
	Root    string
	AgentID string
}

func (c *Codex) Name() string { return "codex:" + c.Root }

type codexFile struct {
	Offset  int64  `json:"o"`
	Session string `json:"s,omitempty"`
	Model   string `json:"m,omitempty"`
	Total   int64  `json:"t,omitempty"`
}

type codexTokens struct {
	InputTokens  int64 `json:"input_tokens"`
	CachedInput  int64 `json:"cached_input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

type codexLine struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Payload   struct {
		Type  string `json:"type"`
		ID    string `json:"id"`
		Model string `json:"model"`
		Info  *struct {
			Total *codexTokens `json:"total_token_usage"`
			Last  *codexTokens `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

func (c *Codex) Collect(ctx context.Context, cursor string, _ Totaler) ([]Record, string, error) {
	files := map[string]codexFile{}
	if err := loadCursor(cursor, &files); err != nil {
		return nil, cursor, err
	}
	var out []Record
	err := walkJSONL(ctx, c.Root, func(fsys fs.FS, name string) error {
		st := files[name]
		if fi, err := fs.Stat(fsys, name); err != nil {
			return err
		} else if fi.Size() < st.Offset {
			st = codexFile{}
		}
		if st.Session == "" {
			st.Session = strings.TrimSuffix(path.Base(name), ".jsonl")
		}
		off, err := tail(fsys, name, st.Offset, func(line []byte) {
			var l codexLine
			if json.Unmarshal(line, &l) != nil {
				return
			}
			switch {
			case l.Type == "session_meta" && l.Payload.ID != "":
				st.Session = l.Payload.ID
			case l.Type == "turn_context" && l.Payload.Model != "":
				st.Model = l.Payload.Model
			case l.Type == "event_msg" && l.Payload.Type == "token_count" && l.Payload.Info != nil &&
				l.Payload.Info.Total != nil && l.Payload.Info.Last != nil:
				if l.Payload.Info.Total.TotalTokens <= st.Total {
					return
				}
				st.Total = l.Payload.Info.Total.TotalTokens
				u := l.Payload.Info.Last
				out = append(out, Record{
					TS: l.Timestamp, AgentID: c.AgentID, Provider: "openai", Model: st.Model,
					TokensIn: max(u.InputTokens-u.CachedInput, 0), TokensOut: u.OutputTokens, CacheRead: u.CachedInput,
					Source: SourceCodex, Session: st.Session, Key: fmt.Sprintf("codex:%s:%d", st.Session, st.Total),
				})
			}
		})
		st.Offset = off
		files[name] = st
		return err
	})
	if err != nil {
		return nil, cursor, err
	}
	next, err := saveCursor(files)
	return out, next, err
}

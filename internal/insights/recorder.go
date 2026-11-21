package insights

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

const (
	DefaultRetention = 7 * 24 * time.Hour
	DefaultMaxRows   = 50000
	maxBuffered      = 4096
	maxTaskID        = 64
	flushEvery       = time.Second
	pruneEvery       = time.Minute
)

type Store interface {
	InsertToolErrors(ctx context.Context, errs []ToolError) error
	ListToolErrors(ctx context.Context, since time.Time, limit int) ([]ToolError, error)
	PruneToolErrors(ctx context.Context, before time.Time, keep int) error
}

type RecorderOptions struct {
	Now       func() time.Time
	Retention time.Duration
	MaxRows   int
	Log       *slog.Logger
}

type Recorder struct {
	st  Store
	opt RecorderOptions

	mu  sync.Mutex
	buf []ToolError
}

func NewRecorder(st Store, o RecorderOptions) *Recorder {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Retention <= 0 {
		o.Retention = DefaultRetention
	}
	if o.MaxRows <= 0 {
		o.MaxRows = DefaultMaxRows
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Recorder{st: st, opt: o}
}

func Fingerprint(tool, code, message string) string {
	sum := sha256.Sum256([]byte(tool + "\x00" + code + "\x00" + message))
	return hex.EncodeToString(sum[:8])
}

func (r *Recorder) Observe(agentID, tool, code, message string, args json.RawMessage) {
	var a struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(args, &a)
	if len(a.TaskID) > maxTaskID {
		a.TaskID = ""
	}
	e := ToolError{
		TS: r.opt.Now().UTC(), AgentID: agentID, Tool: tool, Code: code,
		TaskID: a.TaskID, Fingerprint: Fingerprint(tool, code, message),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buf) < maxBuffered {
		r.buf = append(r.buf, e)
	}
}

func (r *Recorder) Flush(ctx context.Context) error {
	r.mu.Lock()
	batch := r.buf
	r.buf = nil
	r.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	return r.st.InsertToolErrors(ctx, batch)
}

func (r *Recorder) Prune(ctx context.Context) error {
	return r.st.PruneToolErrors(ctx, r.opt.Now().Add(-r.opt.Retention), r.opt.MaxRows)
}

func (r *Recorder) Run(ctx context.Context) {
	flush := time.NewTicker(flushEvery)
	prune := time.NewTicker(pruneEvery)
	defer flush.Stop()
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-flush.C:
			if err := r.Flush(ctx); err != nil && ctx.Err() == nil {
				r.opt.Log.Warn("insights: storing tool errors failed", "err", err)
			}
		case <-prune.C:
			if err := r.Prune(ctx); err != nil && ctx.Err() == nil {
				r.opt.Log.Warn("insights: pruning tool errors failed", "err", err)
			}
		}
	}
}

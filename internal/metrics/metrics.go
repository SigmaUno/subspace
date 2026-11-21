package metrics

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

var latencyBuckets = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

var agentStatuses = []string{model.AgentActive, model.AgentPaused, model.AgentRevoked}

type Set struct {
	Registry *Registry

	MCPCalls     *CounterVec
	MCPLatency   *HistogramVec
	HTTPRequests *CounterVec
	HTTPLatency  *HistogramVec
	SSEDropped   *CounterVec
	Leases       *CounterVec

	subscribers *GaugeVec
	tasks       *GaugeVec
	agents      *GaugeVec
	backups     *GaugeVec
	backupAt    *GaugeVec
	backupSize  *GaugeVec
}

func New() *Set {
	r := NewRegistry()
	return &Set{
		Registry:     r,
		MCPCalls:     r.NewCounterVec("subspace_mcp_tool_calls_total", "MCP tool calls by tool, caller role and result.", "tool", "role", "result"),
		MCPLatency:   r.NewHistogramVec("subspace_mcp_tool_call_duration_seconds", "MCP tool call latency.", latencyBuckets, "tool", "role"),
		HTTPRequests: r.NewCounterVec("subspace_http_requests_total", "HTTP requests by route pattern and status code.", "route", "code"),
		HTTPLatency:  r.NewHistogramVec("subspace_http_request_duration_seconds", "HTTP request latency by route pattern, excluding the event stream.", latencyBuckets, "route"),
		SSEDropped:   r.NewCounterVec("subspace_sse_dropped_events_total", "Events dropped for slow SSE subscribers."),
		Leases:       r.NewCounterVec("subspace_leases_expired_total", "Task leases expired by the sweeper."),
		subscribers:  r.NewGaugeVec("subspace_sse_subscribers", "Connected SSE subscribers."),
		tasks:        r.NewGaugeVec("subspace_tasks", "Tasks by status.", "status"),
		agents:       r.NewGaugeVec("subspace_agents", "Agents by status.", "status"),
		backups:      r.NewGaugeVec("subspace_backups", "Database backups on disk."),
		backupAt:     r.NewGaugeVec("subspace_backup_last_timestamp_seconds", "Modification time of the newest backup."),
		backupSize:   r.NewGaugeVec("subspace_backup_last_bytes", "Size of the newest backup."),
	}
}

type Source struct {
	Store     store.Store
	Hub       events.Hub
	BackupDir string
	Log       *slog.Logger
}

func (s *Set) Collect(src Source) {
	var (
		mu    sync.Mutex
		since int64
	)
	s.Registry.OnScrape(func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if src.Hub != nil {
			s.subscribers.With().Set(float64(src.Hub.Subscribers()))
		}
		if src.Store != nil {
			mu.Lock()
			defer mu.Unlock()
			s.collectStore(ctx, src, &since)
		}
		s.collectBackups(src.BackupDir)
	})
}

func (s *Set) collectStore(ctx context.Context, src Source, since *int64) {
	var (
		counts   map[string]int
		byStatus = map[string]int{}
		expired  uint64
		cursor   = *since
	)
	err := src.Store.View(ctx, func(tx store.Tx) error {
		var err error
		if counts, err = tx.CountTasksByStatus(); err != nil {
			return err
		}
		ags, err := tx.ListAgents(store.AgentFilter{Limit: 1 << 20})
		if err != nil {
			return err
		}
		for _, a := range ags {
			byStatus[a.Status]++
		}
		for {
			evs, err := tx.ListAudit(store.AuditFilter{AgentID: "system", Tool: "lease_release", SinceID: cursor, Limit: 1000})
			if err != nil {
				return err
			}
			for _, e := range evs {
				cursor = e.ID
				if e.Note != nil && *e.Note == "lease expired" {
					expired++
				}
			}
			if len(evs) < 1000 {
				return nil
			}
		}
	})
	if err != nil {
		if src.Log != nil {
			src.Log.Warn("metrics collect", "err", err)
		}
		return
	}
	for _, st := range model.AllStatuses {
		s.tasks.With(st).Set(float64(counts[st]))
	}
	for _, st := range agentStatuses {
		s.agents.With(st).Set(float64(byStatus[st]))
	}
	s.Leases.With().Add(expired)
	*since = cursor
}

func (s *Set) collectBackups(dir string) {
	if dir == "" {
		return
	}
	es, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return
	}
	var (
		n    int
		last time.Time
		size int64
	)
	for _, e := range es {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "subspace-") || filepath.Ext(name) != ".db" {
			continue
		}
		n++
		if info, err := e.Info(); err == nil && info.ModTime().After(last) {
			last, size = info.ModTime(), info.Size()
		}
	}
	var at float64
	if n > 0 {
		at = float64(last.Unix())
	}
	s.backups.With().Set(float64(n))
	s.backupAt.With().Set(at)
	s.backupSize.With().Set(float64(size))
}

var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

func getBuf() *bytes.Buffer { return bufPool.Get().(*bytes.Buffer) }

func putBuf(b *bytes.Buffer) {
	if b.Cap() > 64<<10 {
		return
	}
	b.Reset()
	bufPool.Put(b)
}

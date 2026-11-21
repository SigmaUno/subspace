package server

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/SigmaUno/subspace/internal/agentout"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
)

const defaultRateOutput = "10/s:60"

var agentOutputs sync.Map

func init() {
	registerMount(mountAgentOutput)
	wraps = append(wraps, wrapAgentOutput)
}

func mountAgentOutput(d Deps) {
	cfg := agentout.DefaultConfig()
	cfg.Rate = envRate(d.Log, d.getenv(), "SUBSPACE_OUTPUT_RATE", defaultRateOutput)
	cfg.Limits.Lines = envInt(d.Log, "SUBSPACE_OUTPUT_LINES", cfg.Limits.Lines, 1)
	cfg.Limits.Bytes = envInt(d.Log, "SUBSPACE_OUTPUT_AGENT_BYTES", cfg.Limits.Bytes, agentout.MaxChunk)
	cfg.Limits.GlobalBytes = envInt(d.Log, "SUBSPACE_OUTPUT_MEMORY", cfg.Limits.GlobalBytes, cfg.Limits.Bytes)
	cfg.Persist = envInt(d.Log, "SUBSPACE_OUTPUT_PERSIST", cfg.Persist, 0)
	if d.Cfg.FlushEvery > 0 {
		cfg.FlushEvery = d.Cfg.FlushEvery
	}
	cfg.Now, cfg.Log = d.Cfg.Now, d.Log
	cfg.Scan = func(ctx context.Context, a model.Actor, text string) string {
		return d.Board.Scan(ctx, a, board.ChannelOutput, text)
	}
	svc := agentout.New(d.Store, outputDirectory{d.Board}, cfg)
	if err := svc.Restore(d.Ctx); err != nil {
		d.Log.Warn("agent output: loading persisted tail failed", "err", err)
	}
	go svc.Run(d.Ctx)
	mcp.RegisterOutput(d.Registry, svc)
	d.API.Mount(d.Mux, "GET /api/v1/agents/{id}/output", svc.OutputHandler())
	d.API.Mount(d.Mux, "GET /api/v1/agent-output", svc.LatestHandler())
	agentOutputs.Store(d.Mux, svc)
	context.AfterFunc(d.Ctx, func() {
		agentOutputs.Delete(d.Mux)
	})
}

func wrapAgentOutput(next http.Handler, d Deps) http.Handler {
	v, ok := agentOutputs.Load(d.Mux)
	if !ok {
		return next
	}
	stream := http.NewServeMux()
	d.API.Mount(stream, "GET /api/v1/events", v.(*agentout.Service).StreamHandler())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/events" {
			if output, _ := agentout.SplitTopics(events.TopicsFromQuery(r)); output {
				stream.ServeHTTP(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func envInt(log *slog.Logger, key string, def, minimum int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < minimum {
		log.Warn("invalid setting, using default", "env", key, "default", def)
		return def
	}
	return n
}

type outputDirectory struct{ b *board.Board }

func (o outputDirectory) Agent(ctx context.Context, id string) error {
	_, err := o.b.GetAgent(ctx, id)
	return err
}

func (o outputDirectory) Active(ctx context.Context) ([]string, error) {
	as, err := o.b.ListAgents(ctx, "", model.AgentActive)
	ids := make([]string, 0, len(as))
	for _, a := range as {
		ids = append(ids, a.ID)
	}
	return ids, err
}

func (o outputDirectory) CanSeeTask(ctx context.Context, a model.Actor, taskID string) error {
	_, err := o.b.GetTask(ctx, a, taskID)
	return err
}

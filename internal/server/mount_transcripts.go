package server

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/agentout"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/transcript"
)

const (
	envTranscriptMaxMB = "SUBSPACE_TRANSCRIPT_MAX_MB"
	envTranscriptFlush = "SUBSPACE_TRANSCRIPT_FLUSH"
)

var transcriptServices sync.Map

type transcriptTasks struct{ b *board.Board }

func (t transcriptTasks) Task(ctx context.Context, taskID string) (transcript.Info, error) {
	task, err := t.b.GetTask(ctx, model.Owner, taskID)
	if err != nil {
		return transcript.Info{}, err
	}
	return transcript.Info{BriefID: task.BriefID, Status: task.Status}, nil
}

func init() { registerMount(mountTranscripts) }

func dataSubdir(d Deps, name string) string {
	dir := d.Cfg.DataDir
	if dir == "" && !isPostgres(d.Cfg.DBPath) {
		dir = filepath.Dir(d.Cfg.DBPath)
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, name)
}

func transcriptConfig(d Deps) (transcript.Config, error) {
	cfg := transcript.DefaultConfig()
	cfg.Dir = dataSubdir(d, "transcripts")
	if cfg.Dir == "" {
		return cfg, fmt.Errorf("SUBSPACE_DATA_DIR is required with the postgres backend")
	}
	getenv := d.getenv()
	if v := getenv(envTranscriptMaxMB); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return cfg, fmt.Errorf("%s must be a non-negative integer (0 = unlimited)", envTranscriptMaxMB)
		}
		cfg.MaxBytes = n << 20
	}
	if v := getenv(envTranscriptFlush); v != "" {
		n, err := time.ParseDuration(v)
		if err != nil || n <= 0 {
			return cfg, fmt.Errorf("%s must be a positive duration such as 5m", envTranscriptFlush)
		}
		cfg.FlushEvery = n
	}
	if d.Cfg.FlushEvery > 0 {
		cfg.CheckEvery = d.Cfg.FlushEvery
	}
	cfg.Now, cfg.Log = d.Cfg.Now, d.Log
	return cfg, nil
}

func mountTranscripts(d Deps) {
	v, ok := agentOutputs.Load(d.Mux)
	if !ok {
		return
	}
	cfg, err := transcriptConfig(d)
	if err != nil {
		d.Log.Error("task transcripts disabled", "err", err)
		return
	}
	svc, err := transcript.New(d.Store, transcriptTasks{d.Board}, cfg)
	if err != nil {
		d.Log.Error("task transcripts disabled", "err", err)
		return
	}
	v.(*agentout.Service).SetSink(svc.Add)
	go svc.Run(d.Ctx)
	if d.OnClose != nil {
		d.OnClose(func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(d.Ctx), 10*time.Second)
			defer cancel()
			if err := svc.Flush(ctx, ""); err != nil {
				d.Log.Warn("task transcripts: final flush failed", "err", err)
			}
		})
	}
	h := transcript.Handler(svc)
	d.API.Mount(d.Mux, "GET /api/v1/tasks/{id}/transcript", h)
	d.API.Mount(d.Mux, "GET /api/v1/tasks/{id}/transcript/download", h)
	transcriptServices.Store(d.Mux, svc)
	go func() {
		<-d.Ctx.Done()
		transcriptServices.Delete(d.Mux)
	}()
}

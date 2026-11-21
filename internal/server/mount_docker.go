package server

import (
	"context"

	"github.com/SigmaUno/subspace/internal/agentout"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/redact"
	"github.com/SigmaUno/subspace/internal/spawner"
)

func init() {
	registerMount(mountDocker)
}

func mountDocker(d Deps) {
	dk, ok := d.Cfg.Spawner.(*spawner.Docker)
	if !ok {
		return
	}
	v, ok := agentOutputs.Load(d.Mux)
	if !ok {
		return
	}
	svc := v.(*agentout.Service)
	d.Leader.Go(d.Ctx, func(ctx context.Context) {
		rep, err := dk.Cleanup(ctx)
		if err != nil {
			d.Log.Warn("docker spawner: cleanup failed", "err", err)
		} else {
			d.Log.Info("docker spawner: cleanup", "containers", rep.Containers, "networks", rep.Networks, "volumes", rep.Volumes, "secrets", rep.Secrets)
		}
		dk.WatchLogs(ctx, func(agentID, stream, text string) {
			a, err := d.Board.GetAgent(ctx, agentID)
			if err != nil {
				return
			}
			actor := model.Actor{ID: a.ID, Role: a.Role, Kind: a.Kind}
			text = redact.String(d.Board.Scan(ctx, actor, board.ChannelOutput, text))
			if _, err := svc.Append(ctx, actor, agentout.Input{Stream: stream, Text: text}); err != nil && ctx.Err() == nil {
				d.Log.Debug("docker spawner: log capture dropped", "agent_id", agentID, "err", err)
			}
		})
	})
}

package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/ha"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/notify"
)

const (
	bridgeReadyWait = 5 * time.Second
	keyLockConns    = 16
)

type cluster struct {
	elector *ha.Elector
	bridge  *ha.Bridge
	locker  *ha.KeyLocker
}

func instanceID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "subspace"
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return host + "-" + hex.EncodeToString(b)
}

func newCluster(cfg Config, local events.Hub, onRemote func([]string, string, any)) (*cluster, error) {
	if !isPostgres(cfg.DBPath) {
		return &cluster{elector: ha.New(ha.Options{ID: cfg.InstanceID, Log: cfg.Log, Now: cfg.Now})}, nil
	}
	kl, err := ha.NewKeyLocker(cfg.DBPath, keyLockConns)
	if err != nil {
		return nil, err
	}
	lock := ha.NewPGLock(cfg.DBPath, cfg.InstanceID, "leader", max(cfg.LeaderTTL, ha.DefaultTTL))
	return &cluster{
		elector: ha.New(ha.Options{ID: cfg.InstanceID, Backend: "postgres", Lock: lock,
			Interval: cfg.LeaderInterval, TTL: cfg.LeaderTTL, Log: cfg.Log, Now: cfg.Now}),
		bridge: ha.NewBridge(ha.BridgeOptions{ID: cfg.InstanceID, DSN: cfg.DBPath, Local: local,
			Decode: decodeEvent, OnRemote: onRemote, Log: cfg.Log}),
		locker: kl,
	}, nil
}

func (c *cluster) hub(local events.Hub) events.Hub {
	if c.bridge != nil {
		return c.bridge
	}
	return local
}

func (c *cluster) start(ctx context.Context) {
	go c.elector.Run(ctx)
	if c.bridge == nil {
		return
	}
	go c.bridge.Run(ctx)
	t := time.NewTimer(bridgeReadyWait)
	defer t.Stop()
	select {
	case <-c.bridge.Ready():
	case <-t.C:
	case <-ctx.Done():
	}
}

func (c *cluster) keyLock() func(context.Context, string) (func(), error) {
	if c.locker == nil {
		return nil
	}
	return c.locker.Lock
}

func (c *cluster) close() {
	if c.locker != nil {
		_ = c.locker.Close()
	}
}

func decodeEvent(typ string, raw json.RawMessage) any {
	var v any
	switch typ {
	case "task.created", "task.updated":
		v = &model.Task{}
	case "message.created":
		v = &model.Message{}
	case "question.created", "question.answered":
		v = &model.Question{}
	case "plan.proposed", "plan.approved", "plan.rejected":
		v = &model.Plan{}
	case "agent.updated":
		v = &model.Agent{}
	case "alert.raised", "alert.resolved":
		var n notify.Notification
		if json.Unmarshal(raw, &n) == nil {
			return n
		}
	}
	if v != nil && json.Unmarshal(raw, v) == nil {
		return v
	}
	var g any
	_ = json.Unmarshal(raw, &g)
	return g
}

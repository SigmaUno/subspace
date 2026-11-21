package server

import (
	"time"

	"github.com/SigmaUno/subspace/internal/mcp"
)

const (
	mcpPushBufferEnv = "SUBSPACE_MCP_PUSH_BUFFER"
	mcpPushPingEnv   = "SUBSPACE_MCP_PUSH_PING"
	mcpPushOffEnv    = "SUBSPACE_MCP_PUSH"
)

func init() { registerMount(mountMCPPush) }

func mountMCPPush(d Deps) {
	if d.MCP == nil || d.getenv()(mcpPushOffEnv) == "off" {
		return
	}
	p := mcp.NewPush()
	p.Buffer = envInt(d.Log, mcpPushBufferEnv, mcp.DefaultPushBuffer, 1)
	p.Ping = mcp.DefaultPushPing
	if v := d.getenv()(mcpPushPingEnv); v != "" {
		if dur, err := time.ParseDuration(v); err == nil && dur > 0 {
			p.Ping = dur
		} else {
			d.Log.Warn("invalid setting, using default", "env", mcpPushPingEnv, "default", mcp.DefaultPushPing)
		}
	}
	p.Tasks = callbackOwners{d.Board}
	if subs, ok := d.MCP.Sessions.(mcp.SubscriptionStore); ok {
		p.Subs = subs
	}
	w := &mcp.PushWatcher{Push: p}
	p.Start = func() { w.Activate(d.Ctx, d.Hub) }
	d.MCP.Push = p
}

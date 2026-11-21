package server

import (
	"path/filepath"
	"strings"

	"github.com/SigmaUno/subspace/internal/gateway"
	"github.com/SigmaUno/subspace/internal/netguard"
)

const (
	envGatewayCommands = "SUBSPACE_GATEWAY_COMMANDS"
	envGatewayAllow    = "SUBSPACE_GATEWAY_ALLOW_CIDRS"
)

func init() { registerMount(mountGateways) }

func gatewayCommands(raw string) []string {
	var out []string
	for _, c := range strings.Split(raw, ",") {
		c = strings.TrimSpace(c)
		if c != "" && filepath.IsAbs(c) && filepath.Clean(c) == c {
			out = append(out, c)
		}
	}
	return out
}

func mountGateways(d Deps) {
	if !gateway.SupportsSQL(d.Ctx, d.Store) {
		d.Log.Error("mcp gateways disabled", "err", "store does not support SQL extensions")
		return
	}
	env := d.getenv()
	allow, err := netguard.ParsePrefixes(env(envGatewayAllow))
	if err != nil {
		d.Log.Error("gateway network allowlist ignored", "err", err)
		allow = nil
	}
	svc := gateway.New(gateway.Options{
		Store:    d.Store,
		Commands: gatewayCommands(env(envGatewayCommands)),
		Guard:    netguard.New(allow),
		Getenv:   env,
		Now:      d.Cfg.Now,
		Log:      d.Log,
	})
	d.Registry.AddSource(svc)
	h := svc.Handler()
	d.API.Mount(d.Mux, "/api/v1/gateways", h)
	d.API.Mount(d.Mux, "/api/v1/gateways/", h)
	go func() {
		<-d.Ctx.Done()
		svc.Close()
	}()
}

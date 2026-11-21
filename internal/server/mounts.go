package server

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/SigmaUno/subspace/internal/api"
	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/breaker"
	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/ha"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/reload"
	"github.com/SigmaUno/subspace/internal/store"
)

type Deps struct {
	Ctx   context.Context
	Board *board.Board
	Store store.Store
	Hub   events.Hub
	Cfg   Config
	Log   *slog.Logger
	API   *api.API
	Mux   *http.ServeMux
	Auth  auth.AgentLookup
	Feed  *decisions.Feed

	MCP        *mcp.Server
	Registry   *mcp.Registry
	Breakers   *breaker.Set
	Components components.Store
	LocalHub   events.Hub
	Leader     *ha.Elector
	KeyLock    func(ctx context.Context, key string) (func(), error)

	Env    func(string) string
	Reload *reload.Manager
	Trust  *proxyTrust
}

func (d Deps) getenv() func(string) string {
	if d.Env != nil {
		return d.Env
	}
	return os.Getenv
}

func (d Deps) reloadable(name string, keys []string, r reload.Reloadable) {
	if d.Reload != nil {
		d.Reload.Register(name, keys, r)
	}
}

var extraMounts []func(Deps)

var wraps []func(http.Handler, Deps) http.Handler

func registerMount(f func(Deps)) { extraMounts = append(extraMounts, f) } //nolint:unused

func unbindOnDone(d Deps, unbind func()) { context.AfterFunc(d.Ctx, unbind) }

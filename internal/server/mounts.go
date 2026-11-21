package server

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/SigmaUno/subspace/internal/api"
	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/breaker"
	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/ha"
	"github.com/SigmaUno/subspace/internal/mcp"
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

	Registry   *mcp.Registry
	Breakers   *breaker.Set
	Components components.Store
	LocalHub   events.Hub
	Leader     *ha.Elector
	KeyLock    func(ctx context.Context, key string) (func(), error)
}

var extraMounts []func(Deps)

var wraps []func(http.Handler, Deps) http.Handler

func registerMount(f func(Deps)) { extraMounts = append(extraMounts, f) } //nolint:unused

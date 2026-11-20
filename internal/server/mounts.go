package server

import (
	"log/slog"
	"net/http"

	"github.com/SigmaUno/subspace/internal/api"
	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/store"
)

type Deps struct {
	Board *board.Board
	Store store.Store
	Hub   events.Hub
	Cfg   Config
	Log   *slog.Logger
	API   *api.API
	Mux   *http.ServeMux
	Auth  auth.AgentLookup
}

var extraMounts []func(Deps)

var wraps []func(http.Handler, Deps) http.Handler

func registerMount(f func(Deps)) { extraMounts = append(extraMounts, f) } //nolint:unused

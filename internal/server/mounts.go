package server

import (
	"log/slog"
	"net/http"

	"github.com/SigmaUno/subspace/internal/api"
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
}

var extraMounts []func(Deps)

func registerMount(f func(Deps)) { extraMounts = append(extraMounts, f) }

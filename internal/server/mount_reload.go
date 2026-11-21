package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/reload"
)

func init() {
	registerMount(func(d Deps) {
		d.API.Mount(d.Mux, "POST /api/v1/reload", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			res, err := reloadConfig(r.Context(), d.Reload, d.Board, d.Log, "")
			var rejected *reload.Error
			if errors.As(err, &rejected) {
				writeAPIJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{
					"code": "bad_request", "message": rejected.Error(), "errors": rejected.Errors,
				}})
				return
			}
			if err != nil {
				writeAPIError(w, err)
				return
			}
			writeAPIJSON(w, http.StatusOK, res)
		}))
	})
}

func reloadConfig(ctx context.Context, m *reload.Manager, b *board.Board, log *slog.Logger, note string) (reload.Result, error) {
	res, err := m.Reload()
	if err != nil {
		return res, err
	}
	if err := b.AuditReload(ctx, res, note); err != nil {
		log.Error("audit config reload", "err", err)
	}
	return res, nil
}

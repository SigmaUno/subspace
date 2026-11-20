package api

import (
	"encoding/json"
	"math"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const exportPage = 1000

type exportChain struct {
	Since    int64           `json:"since"`
	PrevHash string          `json:"prev_hash"`
	Head     model.AuditHead `json:"head"`
}

func (a *API) exportAudit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	since := qint64(r, "since")
	if since < 0 {
		a.fail(w, model.BadRequest("since must be >= 0"))
		return
	}
	prev, err := a.B.AuditHead(ctx, since)
	if err != nil {
		a.fail(w, err)
		return
	}
	head, err := a.B.AuditHead(ctx, math.MaxInt64)
	if err != nil {
		a.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	fl, _ := w.(http.Flusher)
	if err := enc.Encode(map[string]exportChain{"chain": {Since: since, PrevHash: prev.Hash, Head: head}}); err != nil {
		return
	}
	for cursor := since; cursor < head.ID; {
		page, err := a.B.ListAudit(ctx, store.AuditFilter{SinceID: cursor, Limit: exportPage})
		if err != nil {
			if a.Log != nil {
				a.Log.Error("audit export", "err", err)
			}
			return
		}
		if len(page) == 0 {
			return
		}
		for _, e := range page {
			if e.ID > head.ID {
				return
			}
			if err := enc.Encode(e); err != nil {
				return
			}
			cursor = e.ID
		}
		if fl != nil {
			fl.Flush()
		}
	}
}

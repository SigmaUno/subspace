package usage

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type handler struct {
	st  store.Store
	b   *board.Board
	log *slog.Logger
}

func Handler(st store.Store, b *board.Board, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	h := &handler{st: st, b: b, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /usage/summary", h.summary)
	mux.HandleFunc("GET /usage/providers", h.providers)
	return mux
}

type Totals struct {
	Calls        int64   `json:"calls"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostEUR      float64 `json:"cost_eur"`
}

type Summary struct {
	Since      *time.Time         `json:"since"`
	Totals     Totals             `json:"totals"`
	ByProvider []store.UsageTotal `json:"by_provider"`
	ByModel    []store.UsageTotal `json:"by_model"`
	ByAgent    []store.UsageTotal `json:"by_agent"`
	ByBrief    []store.UsageTotal `json:"by_brief"`
	ByDay      []store.UsageTotal `json:"by_day"`
}

type Provider struct {
	store.ProviderStat
	Healthy bool              `json:"healthy"`
	Limits  []capacity.Report `json:"limits"`
}

type ProviderView struct {
	Providers       []Provider `json:"providers"`
	GlobalMaxAgents int        `json:"global_max_agents"`
	AgentsInUse     int        `json:"agents_in_use"`
}

func (h *handler) summary(w http.ResponseWriter, r *http.Request) {
	var since time.Time
	s := &Summary{}
	if v := r.URL.Query().Get("since"); v != "" {
		ts, err := time.Parse(time.RFC3339, v)
		if err != nil {
			h.fail(w, model.BadRequest("since must be an RFC 3339 timestamp"))
			return
		}
		since = ts
		s.Since = &ts
	}
	err := h.st.View(r.Context(), func(tx store.Tx) error {
		ut, ok := tx.(store.UsageTx)
		if !ok {
			return model.Errf("internal", "store does not support usage tracking")
		}
		for _, g := range []struct {
			by  store.UsageGroup
			dst *[]store.UsageTotal
		}{
			{store.UsageByProvider, &s.ByProvider}, {store.UsageByModel, &s.ByModel}, {store.UsageByAgent, &s.ByAgent},
			{store.UsageByBrief, &s.ByBrief}, {store.UsageByDay, &s.ByDay},
		} {
			rows, err := ut.UsageTotals(g.by, since)
			if err != nil {
				return err
			}
			*g.dst = rows
		}
		return nil
	})
	if err != nil {
		h.fail(w, err)
		return
	}
	for _, p := range s.ByProvider {
		s.Totals.Calls += p.Calls
		s.Totals.InputTokens += p.InputTokens
		s.Totals.OutputTokens += p.OutputTokens
		s.Totals.CostEUR += p.CostEUR
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *handler) providers(w http.ResponseWriter, r *http.Request) {
	v, err := h.providerView(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *handler) providerView(ctx context.Context) (*ProviderView, error) {
	var stats []store.ProviderStat
	err := h.st.View(ctx, func(tx store.Tx) error {
		ut, ok := tx.(store.UsageTx)
		if !ok {
			return model.Errf("internal", "store does not support usage tracking")
		}
		var err error
		stats, err = ut.ProviderStats()
		return err
	})
	if err != nil {
		return nil, err
	}
	cv, err := h.b.Capacity(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, s := range stats {
		seen[s.Provider] = true
	}
	for _, l := range cv.Limits {
		if l.Provider != "*" && !seen[l.Provider] {
			stats = append(stats, store.ProviderStat{Provider: l.Provider, Model: l.Model})
		}
	}
	sort.SliceStable(stats, func(i, j int) bool {
		if stats[i].Provider != stats[j].Provider {
			return stats[i].Provider < stats[j].Provider
		}
		return stats[i].Model < stats[j].Model
	})
	out := &ProviderView{Providers: make([]Provider, 0, len(stats)), GlobalMaxAgents: cv.GlobalMaxAgents, AgentsInUse: cv.AgentsInUse}
	for _, s := range stats {
		p := Provider{ProviderStat: s, Healthy: s.ErrorsSinceSuccess == 0, Limits: []capacity.Report{}}
		for _, l := range cv.Limits {
			if (l.Provider == "*" || l.Provider == s.Provider) && (l.Model == "*" || l.Model == s.Model) {
				p.Limits = append(p.Limits, l)
			}
		}
		out.Providers = append(out.Providers, p)
	}
	return out, nil
}

func (h *handler) fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	if e.Code == "internal" {
		h.log.Error("usage error", "err", err)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	body.Error.Code, body.Error.Message = e.Code, e.Message
	writeJSON(w, e.HTTPStatus(), body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

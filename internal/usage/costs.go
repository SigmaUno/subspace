package usage

import (
	"context"
	"errors"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const defaultCostWindow = 30 * 24 * time.Hour

type CostTotals struct {
	Requests   int64   `json:"requests"`
	TokensIn   int64   `json:"tokens_in"`
	TokensOut  int64   `json:"tokens_out"`
	CacheRead  int64   `json:"cache_read"`
	CacheWrite int64   `json:"cache_write"`
	CostEUR    float64 `json:"cost_eur"`
}

func (t *CostTotals) add(r store.CostRow) {
	t.Requests += r.Requests
	t.TokensIn += r.TokensIn
	t.TokensOut += r.TokensOut
	t.CacheRead += r.CacheRead
	t.CacheWrite += r.CacheWrite
	t.CostEUR += r.CostEUR
}

func (t *CostTotals) round() { t.CostEUR = math.Round(t.CostEUR*1e6) / 1e6 }

type CostPart struct {
	Key string `json:"key"`
	CostTotals
}

type CostGroup struct {
	Key string `json:"key"`
	CostTotals
	Split []CostPart `json:"split,omitempty"`
}

type CostBreakdown struct {
	GroupBy string      `json:"group_by"`
	SplitBy string      `json:"split_by,omitempty"`
	BriefID string      `json:"brief_id,omitempty"`
	Since   time.Time   `json:"since"`
	Until   time.Time   `json:"until"`
	Total   CostTotals  `json:"total"`
	Groups  []CostGroup `json:"groups"`
}

type CostRequest struct {
	GroupBy string
	SplitBy string
	BriefID string
	Since   time.Time
	Until   time.Time
}

type CostService struct {
	st  store.Store
	now func() time.Time
}

func NewCostService(st store.Store, now func() time.Time) *CostService {
	if now == nil {
		now = time.Now
	}
	return &CostService{st: st, now: now}
}

func (s *CostService) Costs(ctx context.Context, r CostRequest) (CostBreakdown, error) {
	if r.GroupBy == "" {
		r.GroupBy = store.CostByBrief
	}
	if !slices.Contains(store.CostDimensions, r.GroupBy) {
		return CostBreakdown{}, model.BadRequest("group_by must be one of %s", strings.Join(store.CostDimensions, ", "))
	}
	if r.SplitBy != "" && (!slices.Contains(store.CostDimensions, r.SplitBy) || r.SplitBy == r.GroupBy) {
		return CostBreakdown{}, model.BadRequest("split_by must be one of %s and differ from group_by", strings.Join(store.CostDimensions, ", "))
	}
	if r.Until.IsZero() {
		r.Until = s.now().UTC().Truncate(time.Minute).Add(time.Minute)
	}
	if r.Since.IsZero() {
		r.Since = r.Until.Add(-defaultCostWindow)
	}
	if !r.Since.Before(r.Until) {
		return CostBreakdown{}, model.BadRequest("since must be before until")
	}
	var rows []store.CostRow
	err := s.st.View(ctx, func(tx store.Tx) error {
		if r.BriefID != "" {
			if _, err := tx.GetBrief(r.BriefID); errors.Is(err, store.ErrNotFound) {
				return model.NotFound("brief %s not found", r.BriefID)
			} else if err != nil {
				return err
			}
		}
		ct, ok := tx.(store.CostsTx)
		if !ok {
			return errors.New("store does not support costs")
		}
		var err error
		rows, err = ct.Costs(store.CostQuery{GroupBy: r.GroupBy, SplitBy: r.SplitBy, Since: r.Since, Until: r.Until, BriefID: r.BriefID})
		return err
	})
	if err != nil {
		return CostBreakdown{}, err
	}
	out := CostBreakdown{
		GroupBy: r.GroupBy, SplitBy: r.SplitBy, BriefID: r.BriefID,
		Since: r.Since.UTC(), Until: r.Until.UTC(), Groups: []CostGroup{},
	}
	idx := map[string]int{}
	for _, row := range rows {
		out.Total.add(row)
		i, ok := idx[row.Key]
		if !ok {
			i = len(out.Groups)
			idx[row.Key] = i
			out.Groups = append(out.Groups, CostGroup{Key: row.Key})
		}
		g := &out.Groups[i]
		g.add(row)
		if r.SplitBy != "" {
			p := CostPart{Key: row.Sub}
			p.add(row)
			p.round()
			g.Split = append(g.Split, p)
		}
	}
	out.Total.round()
	for i := range out.Groups {
		g := &out.Groups[i]
		g.round()
		slices.SortStableFunc(g.Split, compareParts)
	}
	if r.GroupBy != store.CostByDay {
		slices.SortStableFunc(out.Groups, func(a, b CostGroup) int {
			if a.CostEUR != b.CostEUR {
				return costOrder(a.CostEUR, b.CostEUR)
			}
			return strings.Compare(a.Key, b.Key)
		})
	}
	return out, nil
}

func costOrder(a, b float64) int {
	if a > b {
		return -1
	}
	return 1
}

func compareParts(a, b CostPart) int {
	if a.CostEUR != b.CostEUR {
		return costOrder(a.CostEUR, b.CostEUR)
	}
	return strings.Compare(a.Key, b.Key)
}

func CostsHandler(svc *CostService) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /costs", func(w http.ResponseWriter, r *http.Request) {
		v := r.URL.Query()
		req := CostRequest{GroupBy: v.Get("group_by"), SplitBy: v.Get("split_by"), BriefID: v.Get("brief_id")}
		for _, p := range []struct {
			name string
			dst  *time.Time
		}{{"since", &req.Since}, {"until", &req.Until}} {
			if s := v.Get(p.name); s != "" {
				t, err := time.Parse(time.RFC3339, s)
				if err != nil {
					fail(w, model.BadRequest("%s must be an RFC 3339 timestamp", p.name))
					return
				}
				*p.dst = t.UTC()
			}
		}
		out, err := svc.Costs(r.Context(), req)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	return mux
}

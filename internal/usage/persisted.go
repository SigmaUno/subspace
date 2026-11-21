package usage

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/store"
)

const (
	limitedRetention = 24 * time.Hour
	cursorKeyPrefix  = "usage.cursor."
)

var errNoCosts = errors.New("store does not support costs")

type Persisted struct {
	st       store.Store
	eurToUSD float64

	mu      sync.Mutex
	limited []Record
}

func NewPersisted(st store.Store, eurToUSD float64) *Persisted {
	if eurToUSD <= 0 {
		eurToUSD = 1
	}
	return &Persisted{st: st, eurToUSD: eurToUSD}
}

func (p *Persisted) Put(ctx context.Context, recs []Record) error {
	var done []Record
	p.mu.Lock()
	for _, r := range recs {
		if !r.RateLimited {
			done = append(done, r)
			continue
		}
		cut := r.TS.Add(-limitedRetention)
		p.limited = slices.DeleteFunc(p.limited, func(x Record) bool { return x.TS.Before(cut) || (r.Key != "" && x.Key == r.Key) })
		p.limited = append(p.limited, r)
	}
	p.mu.Unlock()
	if len(done) == 0 {
		return nil
	}
	return p.st.Atomic(ctx, func(tx store.Tx) error {
		ct, ok := tx.(store.CostsTx)
		if !ok {
			return errNoCosts
		}
		ut, _ := tx.(store.TimeseriesTx)
		for _, r := range done {
			err := ct.PutCost(store.CostRecord{
				Key: r.Key, TS: r.TS, BriefID: r.BriefID, TaskID: r.TaskID, ComponentID: r.ComponentID, AgentID: r.AgentID,
				Kind: r.Kind, Role: r.Role, Provider: r.Provider, Model: r.Model,
				TokensIn: r.TokensIn, TokensOut: r.TokensOut, CacheRead: r.CacheRead, CacheWrite: r.CacheWrite,
				CostEUR: r.CostUSD / p.eurToUSD,
			})
			if err != nil {
				return err
			}
			if ut == nil {
				continue
			}
			err = ut.PutUsage(store.UsageRecord{
				Key: r.Key, TS: r.TS, AgentID: r.AgentID, TaskID: r.TaskID, BriefID: r.BriefID, TokensIn: r.TokensIn, TokensOut: r.TokensOut,
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func farFuture() time.Time { return time.UnixMilli(math.MaxInt64 / 2) }

func distantPast() time.Time { return time.UnixMilli(math.MinInt64 / 2) }

func (p *Persisted) Query(ctx context.Context, q Query) ([]Record, error) {
	from, until := q.From, q.To
	if from.IsZero() {
		from = distantPast()
	}
	if until.IsZero() {
		until = farFuture()
	}
	var out []Record
	err := p.st.View(ctx, func(tx store.Tx) error {
		ct, ok := tx.(store.CostsTx)
		if !ok {
			return errNoCosts
		}
		rows, err := ct.ListCosts(from, until)
		if err != nil {
			return err
		}
		for _, c := range rows {
			r := Record{
				TS: c.TS, AgentID: c.AgentID, TaskID: c.TaskID, BriefID: c.BriefID, ComponentID: c.ComponentID,
				Kind: c.Kind, Role: c.Role, Provider: c.Provider, Model: c.Model,
				TokensIn: c.TokensIn, TokensOut: c.TokensOut, CacheRead: c.CacheRead, CacheWrite: c.CacheWrite,
				CostUSD: c.CostEUR * p.eurToUSD, Source: sourceOf(c.Key), Key: c.Key,
			}
			if q.match(&r) {
				out = append(out, r)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.limited {
		if q.match(&p.limited[i]) {
			out = append(out, p.limited[i])
		}
	}
	return out, nil
}

func sourceOf(key string) string {
	src, _, _ := strings.Cut(key, ":")
	switch src {
	case SourceMCP, SourceOpenCode, SourceClaude, SourceCodex:
		return src
	}
	return ""
}

func (p *Persisted) Spent(ctx context.Context) (total float64, byBrief map[string]float64, err error) {
	byBrief = map[string]float64{}
	err = p.st.View(ctx, func(tx store.Tx) error {
		ct, ok := tx.(store.CostsTx)
		if !ok {
			return errNoCosts
		}
		rows, err := ct.Costs(store.CostQuery{GroupBy: store.CostByBrief, Since: distantPast(), Until: farFuture()})
		if err != nil {
			return err
		}
		for _, r := range rows {
			usd := r.CostEUR * p.eurToUSD
			total += usd
			if r.Key != "" {
				byBrief[r.Key] += usd
			}
		}
		return nil
	})
	return
}

func (p *Persisted) SessionTotal(_ context.Context, source, session string) (Record, error) {
	return Record{Source: source, Session: session}, nil
}

func (p *Persisted) Cursor(ctx context.Context, name string) (v string, err error) {
	err = p.st.View(ctx, func(tx store.Tx) error {
		v, _, err = tx.GetKV(cursorKeyPrefix + name)
		return err
	})
	return
}

func (p *Persisted) SetCursor(ctx context.Context, name, value string) error {
	return p.st.Atomic(ctx, func(tx store.Tx) error { return tx.SetKV(cursorKeyPrefix+name, value) })
}

package usage

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/model"
)

type Config struct {
	TotalCreditsUSD float64       `json:"total_credits_usd"`
	EURToUSD        float64       `json:"eur_to_usd"`
	Prices          []Price       `json:"prices"`
	ThrottleWindow  time.Duration `json:"throttle_window"`
}

type CapacitySource interface {
	Capacity(ctx context.Context) (capacity.Config, capacity.Usage, error)
}

type CapacityFunc func(ctx context.Context) (capacity.Config, capacity.Usage, error)

func (f CapacityFunc) Capacity(ctx context.Context) (capacity.Config, capacity.Usage, error) {
	return f(ctx)
}

type BriefSource interface {
	ListBriefs(ctx context.Context, status string) ([]*model.Brief, error)
}

type Service struct {
	Store    Store
	Config   Config
	Capacity CapacitySource
	Briefs   BriefSource
	BriefOf  func(ctx context.Context, taskID string) (string, error)
	Attrib   Attributor
	Now      func() time.Time

	seq atomic.Int64
}

func New(st Store, cfg Config) *Service {
	if cfg.EURToUSD <= 0 {
		cfg.EURToUSD = 1
	}
	if cfg.Prices == nil {
		cfg.Prices = DefaultPrices
	}
	if cfg.ThrottleWindow <= 0 {
		cfg.ThrottleWindow = 5 * time.Minute
	}
	return &Service{Store: st, Config: cfg, Now: time.Now}
}

func (s *Service) now() time.Time { return s.Now().UTC() }

func (s *Service) Record(ctx context.Context, recs ...Record) error {
	for i := range recs {
		r := &recs[i]
		if r.TokensIn < 0 || r.TokensOut < 0 || r.CacheRead < 0 || r.CacheWrite < 0 || r.CostUSD < 0 {
			return model.BadRequest("token counts and cost must be >= 0")
		}
		if r.TS.IsZero() {
			r.TS = s.now()
		}
		r.TS = r.TS.UTC()
		if r.Provider == "" {
			r.Provider = providerFor(r.Model)
		}
		if r.CostUSD == 0 {
			if p, ok := priceFor(s.Config.Prices, r.Model); ok {
				r.CostUSD = p.cost(*r)
			}
		}
		if r.BriefID == "" && r.TaskID != "" && s.BriefOf != nil {
			b, err := s.BriefOf(ctx, r.TaskID)
			if err != nil {
				return err
			}
			r.BriefID = b
		}
		if s.Attrib != nil {
			if err := s.Attrib(ctx, r); err != nil {
				return err
			}
		}
		if r.Key == "" {
			r.Key = r.Source + ":" + strconv.FormatInt(r.TS.UnixNano(), 36) + ":" + strconv.FormatInt(s.seq.Add(1), 36)
		}
	}
	return s.Store.Put(ctx, recs)
}

type Totals struct {
	Requests    int64   `json:"requests"`
	TokensIn    int64   `json:"tokens_in"`
	TokensOut   int64   `json:"tokens_out"`
	CacheRead   int64   `json:"cache_read"`
	CacheWrite  int64   `json:"cache_write"`
	CostUSD     float64 `json:"cost_usd"`
	RateLimited int64   `json:"rate_limited"`
}

func (t *Totals) add(r *Record) {
	if r.RateLimited {
		t.RateLimited++
	} else {
		t.Requests++
	}
	t.TokensIn += r.TokensIn
	t.TokensOut += r.TokensOut
	t.CacheRead += r.CacheRead
	t.CacheWrite += r.CacheWrite
	t.CostUSD += r.CostUSD
}

type Group struct {
	Key string `json:"key"`
	Totals
}

type Point struct {
	TS time.Time `json:"ts"`
	Totals
}

type Series struct {
	Key    string  `json:"key"`
	Points []Point `json:"points"`
}

func groupKey(r *Record, by string) string {
	switch by {
	case "agent":
		return r.AgentID
	case "task":
		return r.TaskID
	case "brief":
		return r.BriefID
	case "model":
		return r.Provider + "/" + r.Model
	case "provider":
		return r.Provider
	case "source":
		return r.Source
	}
	return "all"
}

func validGroup(by string) bool {
	switch by {
	case "", "all", "agent", "task", "brief", "model", "provider", "source":
		return true
	}
	return false
}

func bucketSize(b string) (time.Duration, bool) {
	switch b {
	case "minute":
		return time.Minute, true
	case "hour":
		return time.Hour, true
	case "day":
		return 24 * time.Hour, true
	}
	return 0, false
}

type Credits struct {
	TotalUSD     *float64 `json:"total_usd"`
	SpentUSD     float64  `json:"spent_usd"`
	RemainingUSD *float64 `json:"remaining_usd"`
}

type BriefBudget struct {
	BriefID      string   `json:"brief_id"`
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	BudgetEUR    float64  `json:"budget_eur"`
	BudgetUSD    float64  `json:"budget_usd"`
	SpentUSD     float64  `json:"spent_usd"`
	RemainingUSD *float64 `json:"remaining_usd"`
}

type Summary struct {
	From    time.Time     `json:"from"`
	To      time.Time     `json:"to"`
	GroupBy string        `json:"group_by"`
	Totals  Totals        `json:"totals"`
	Groups  []Group       `json:"groups"`
	Credits Credits       `json:"credits"`
	Briefs  []BriefBudget `json:"briefs"`
}

func (s *Service) Summary(ctx context.Context, q Query, by string) (*Summary, error) {
	if !validGroup(by) {
		return nil, model.BadRequest("invalid group_by %q", by)
	}
	recs, err := s.Store.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	out := &Summary{From: q.From, To: q.To, GroupBy: by, Groups: []Group{}}
	idx := map[string]int{}
	for i := range recs {
		r := &recs[i]
		out.Totals.add(r)
		k := groupKey(r, by)
		j, ok := idx[k]
		if !ok {
			j = len(out.Groups)
			idx[k] = j
			out.Groups = append(out.Groups, Group{Key: k})
		}
		out.Groups[j].add(r)
	}
	sort.Slice(out.Groups, func(i, j int) bool {
		if out.Groups[i].CostUSD != out.Groups[j].CostUSD {
			return out.Groups[i].CostUSD > out.Groups[j].CostUSD
		}
		return out.Groups[i].Key < out.Groups[j].Key
	})
	if out.Credits, out.Briefs, err = s.budgets(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) budgets(ctx context.Context) (Credits, []BriefBudget, error) {
	all, err := s.Store.Query(ctx, Query{})
	if err != nil {
		return Credits{}, nil, err
	}
	var c Credits
	byBrief := map[string]float64{}
	for _, r := range all {
		c.SpentUSD += r.CostUSD
		if r.BriefID != "" {
			byBrief[r.BriefID] += r.CostUSD
		}
	}
	if t := s.Config.TotalCreditsUSD; t > 0 {
		rem := t - c.SpentUSD
		c.TotalUSD, c.RemainingUSD = &t, &rem
	}
	bb := []BriefBudget{}
	if s.Briefs == nil {
		return c, bb, nil
	}
	briefs, err := s.Briefs.ListBriefs(ctx, "")
	if err != nil {
		return c, nil, err
	}
	for _, b := range briefs {
		x := BriefBudget{
			BriefID: b.ID, Title: b.Title, Status: b.Status, BudgetEUR: b.Budget.MaxCostEUR,
			BudgetUSD: b.Budget.MaxCostEUR * s.Config.EURToUSD, SpentUSD: byBrief[b.ID],
		}
		if x.BudgetUSD > 0 {
			rem := x.BudgetUSD - x.SpentUSD
			x.RemainingUSD = &rem
		}
		bb = append(bb, x)
	}
	return c, bb, nil
}

const maxBuckets = 5000

func (s *Service) Series(ctx context.Context, q Query, bucket, by string) ([]Series, error) {
	size, ok := bucketSize(bucket)
	if !ok {
		return nil, model.BadRequest("bucket must be minute, hour or day")
	}
	if !validGroup(by) {
		return nil, model.BadRequest("invalid group_by %q", by)
	}
	from, to := q.From.Truncate(size), q.To
	n := int(to.Sub(from) / size)
	if to.Sub(from)%size != 0 {
		n++
	}
	if n <= 0 || n > maxBuckets {
		return nil, model.BadRequest("range must cover 1..%d buckets", maxBuckets)
	}
	recs, err := s.Store.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	series := map[string][]Point{}
	for i := range recs {
		r := &recs[i]
		k := groupKey(r, by)
		pts, ok := series[k]
		if !ok {
			pts = make([]Point, n)
			for j := range pts {
				pts[j].TS = from.Add(time.Duration(j) * size)
			}
			series[k] = pts
		}
		pts[int(r.TS.Sub(from)/size)].add(r)
	}
	out := make([]Series, 0, len(series))
	for k, pts := range series {
		out = append(out, Series{Key: k, Points: pts})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

type ProviderStatus struct {
	Provider            string     `json:"provider"`
	Status              string     `json:"status"`
	Requests1m          int64      `json:"requests_1m"`
	RateLimited5m       int64      `json:"rate_limited_5m"`
	RateLimited1h       int64      `json:"rate_limited_1h"`
	LastRateLimitedAt   *time.Time `json:"last_rate_limited_at"`
	MaxConcurrency      int        `json:"max_concurrency"`
	InUse               int        `json:"in_use"`
	ConcurrencyHeadroom int        `json:"concurrency_headroom"`
	RequestsPerMin      int        `json:"requests_per_min"`
	RPMHeadroom         int        `json:"rpm_headroom"`
}

func match(pat, v string) bool { return pat == "*" || pat == v }

func usedBy(l capacity.Limit, u capacity.Usage) int {
	n := 0
	for k, c := range u {
		if match(l.Provider, k[0]) && match(l.Model, k[1]) {
			n += c
		}
	}
	return n
}

func headroom(limit, used int) int {
	if limit <= 0 {
		return -1
	}
	return max(limit-used, 0)
}

func (s *Service) capacity(ctx context.Context) (capacity.Config, capacity.Usage, error) {
	if s.Capacity == nil {
		return capacity.Config{}, capacity.Usage{}, nil
	}
	return s.Capacity.Capacity(ctx)
}

func (s *Service) Providers(ctx context.Context) ([]ProviderStatus, error) {
	now := s.now()
	cfg, u, err := s.capacity(ctx)
	if err != nil {
		return nil, err
	}
	recs, err := s.Store.Query(ctx, Query{From: now.Add(-time.Hour), To: now.Add(time.Nanosecond)})
	if err != nil {
		return nil, err
	}
	ps := map[string]*ProviderStatus{}
	get := func(p string) *ProviderStatus {
		x, ok := ps[p]
		if !ok {
			x = &ProviderStatus{Provider: p}
			ps[p] = x
		}
		return x
	}
	for _, l := range cfg.Limits {
		if l.Provider != "*" {
			get(l.Provider)
		}
	}
	for k := range u {
		get(k[0])
	}
	for i := range recs {
		r := &recs[i]
		if r.Provider == "" {
			continue
		}
		x := get(r.Provider)
		age := now.Sub(r.TS)
		switch {
		case r.RateLimited:
			x.RateLimited1h++
			if age < 5*time.Minute {
				x.RateLimited5m++
			}
			if x.LastRateLimitedAt == nil || r.TS.After(*x.LastRateLimitedAt) {
				ts := r.TS
				x.LastRateLimitedAt = &ts
			}
		case age < time.Minute:
			x.Requests1m++
		}
	}
	out := make([]ProviderStatus, 0, len(ps))
	for p, x := range ps {
		l := providerLimit(cfg, p)
		x.MaxConcurrency, x.RequestsPerMin = l.MaxConcurrency, l.RequestsPerMin
		x.InUse = usedBy(capacity.Limit{Provider: p, Model: "*"}, u)
		x.ConcurrencyHeadroom = headroom(l.MaxConcurrency, usedBy(l, u))
		x.RPMHeadroom = headroom(l.RequestsPerMin, int(s.rpm(recs, l, now)))
		switch {
		case x.LastRateLimitedAt != nil && now.Sub(*x.LastRateLimitedAt) < s.Config.ThrottleWindow:
			x.Status = "throttled"
		case x.ConcurrencyHeadroom == 0 || x.RPMHeadroom == 0:
			x.Status = "saturated"
		default:
			x.Status = "ok"
		}
		out = append(out, *x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out, nil
}

func providerLimit(cfg capacity.Config, p string) capacity.Limit {
	var best capacity.Limit
	for _, l := range cfg.Limits {
		if l.Model != "*" {
			continue
		}
		if l.Provider == p {
			return l
		}
		if l.Provider == "*" {
			best = l
		}
	}
	return best
}

func (s *Service) rpm(recs []Record, l capacity.Limit, now time.Time) int64 {
	var n int64
	for i := range recs {
		r := &recs[i]
		if !r.RateLimited && now.Sub(r.TS) < time.Minute && match(l.Provider, r.Provider) && match(l.Model, r.Model) {
			n++
		}
	}
	return n
}

type ModelCapacity struct {
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	InUse          int    `json:"in_use"`
	RequestsPerMin int64  `json:"requests_1m"`
	Spawnable      int    `json:"spawnable"`
	LimitedBy      string `json:"limited_by"`
}

type CapacityReport struct {
	GlobalMaxAgents     int               `json:"global_max_agents"`
	AgentsInUse         int               `json:"agents_in_use"`
	Limits              []capacity.Report `json:"limits"`
	Models              []ModelCapacity   `json:"models"`
	CreditsRemainingUSD *float64          `json:"credits_remaining_usd"`
}

func (s *Service) LiveCapacity(ctx context.Context) (*CapacityReport, error) {
	now := s.now()
	cfg, u, err := s.capacity(ctx)
	if err != nil {
		return nil, err
	}
	recs, err := s.Store.Query(ctx, Query{From: now.Add(-24 * time.Hour), To: now.Add(time.Nanosecond)})
	if err != nil {
		return nil, err
	}
	credits, _, err := s.budgets(ctx)
	if err != nil {
		return nil, err
	}
	total := 0
	for _, c := range u {
		total += c
	}
	out := &CapacityReport{
		GlobalMaxAgents: cfg.GlobalMaxAgents, AgentsInUse: total, Limits: cfg.Report(u),
		CreditsRemainingUSD: credits.RemainingUSD,
	}
	pairs := map[[2]string]bool{}
	for k := range u {
		pairs[k] = true
	}
	for _, l := range cfg.Limits {
		if l.Provider != "*" && l.Model != "*" {
			pairs[[2]string{l.Provider, l.Model}] = true
		}
	}
	for _, r := range recs {
		if r.Provider != "" && r.Model != "" {
			pairs[[2]string{r.Provider, r.Model}] = true
		}
	}
	for k := range pairs {
		out.Models = append(out.Models, s.spawnable(cfg, u, recs, k, total, credits, now))
	}
	sort.Slice(out.Models, func(i, j int) bool {
		if out.Models[i].Provider != out.Models[j].Provider {
			return out.Models[i].Provider < out.Models[j].Provider
		}
		return out.Models[i].Model < out.Models[j].Model
	})
	return out, nil
}

func (s *Service) spawnable(cfg capacity.Config, u capacity.Usage, recs []Record, k [2]string, total int, credits Credits, now time.Time) ModelCapacity {
	pair := capacity.Limit{Provider: k[0], Model: k[1]}
	m := ModelCapacity{Provider: k[0], Model: k[1], InUse: u[k], RequestsPerMin: s.rpm(recs, pair, now), Spawnable: -1}
	limit := func(n int, why string) {
		n = max(n, 0)
		if m.Spawnable < 0 || n < m.Spawnable {
			m.Spawnable, m.LimitedBy = n, why
		}
	}
	if credits.RemainingUSD != nil && *credits.RemainingUSD <= 0 {
		limit(0, "credits")
	}
	for _, r := range recs {
		if r.RateLimited && r.Provider == k[0] && (r.Model == "" || r.Model == k[1]) && now.Sub(r.TS) < s.Config.ThrottleWindow {
			limit(0, "rate_limited")
			break
		}
	}
	if cfg.GlobalMaxAgents > 0 {
		limit(cfg.GlobalMaxAgents-total, "global")
	}
	for _, l := range cfg.Limits {
		if !match(l.Provider, k[0]) || !match(l.Model, k[1]) {
			continue
		}
		used := usedBy(l, u)
		if l.MaxConcurrency > 0 {
			limit(l.MaxConcurrency-used, fmt.Sprintf("concurrency %s/%s", l.Provider, l.Model))
		}
		if l.RequestsPerMin > 0 {
			rpm := s.rpm(recs, l, now)
			why := fmt.Sprintf("rpm %s/%s", l.Provider, l.Model)
			switch {
			case rpm >= int64(l.RequestsPerMin):
				limit(0, why)
			case rpm > 0 && used > 0:
				per := float64(rpm) / float64(used)
				limit(int(math.Floor(float64(int64(l.RequestsPerMin)-rpm)/per)), why)
			}
		}
	}
	return m
}

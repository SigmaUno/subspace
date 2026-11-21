package breaker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/llm"
)

const (
	EnvProbeInterval     = "SUBSPACE_PROVIDER_PROBE_INTERVAL"
	DefaultProbeInterval = 5 * time.Minute
	defaultProbeTimeout  = 30 * time.Second
)

func ProbeIntervalFromEnv(getenv func(string) string) (time.Duration, error) {
	v := getenv(EnvProbeInterval)
	if v == "" {
		return DefaultProbeInterval, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s must be a duration (0 = off)", EnvProbeInterval)
	}
	return d, nil
}

type ProbeResult struct {
	Provider  string     `json:"provider"`
	Model     string     `json:"model"`
	OK        bool       `json:"ok"`
	LatencyMS int64      `json:"latency_ms"`
	CheckedAt *time.Time `json:"checked_at"`
	Error     string     `json:"error,omitempty"`
	Breaker   State      `json:"breaker"`
}

type Prober struct {
	Set     *Set
	Targets func(ctx context.Context) ([]capacity.Limit, error)
	Getenv  func(string) string
	Client  *http.Client
	Timeout time.Duration
	Now     func() time.Time

	mu   sync.Mutex
	last map[Key]ProbeResult
}

func (p *Prober) targets(ctx context.Context) ([]capacity.Limit, error) {
	ls, err := p.Targets(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[Key]bool{}
	out := ls[:0:0]
	for _, l := range ls {
		k := Key{l.Provider, l.Model}
		if !l.Probeable() || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Model < out[j].Model
	})
	return out, nil
}

func (p *Prober) Probe(ctx context.Context) ([]ProbeResult, error) {
	ls, err := p.targets(ctx)
	if err != nil {
		return nil, err
	}
	var wg sync.WaitGroup
	for _, l := range ls {
		wg.Go(func() { p.probe(ctx, l) })
	}
	wg.Wait()
	return p.list(ls), nil
}

func (p *Prober) List(ctx context.Context) ([]ProbeResult, error) {
	ls, err := p.targets(ctx)
	if err != nil {
		return nil, err
	}
	return p.list(ls), nil
}

func (p *Prober) list(ls []capacity.Limit) []ProbeResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ProbeResult, 0, len(ls))
	for _, l := range ls {
		r, ok := p.last[Key{l.Provider, l.Model}]
		if !ok {
			r = ProbeResult{Provider: l.Provider, Model: l.Model}
		}
		r.Breaker = p.Set.State(l.Provider, l.Model)
		out = append(out, r)
	}
	return out
}

func (p *Prober) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		_, _ = p.Probe(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (p *Prober) probe(ctx context.Context, l capacity.Limit) {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	r := ProbeResult{Provider: l.Provider, Model: l.Model}
	key := strings.TrimSpace(p.Getenv(l.KeyEnv))
	kind := apiKind(l)
	sample := false
	switch {
	case key == "":
		r.Error = l.KeyEnv + " is not set"
	case kind == "":
		r.Error = "provider " + l.Provider + " needs a base_url of an OpenAI-compatible API"
	default:
		start := time.Now()
		err := complete(ctx, kind, l, key, p.Client, timeout)
		r.LatencyMS = time.Since(start).Milliseconds()
		r.OK = err == nil
		if err != nil {
			r.Error = strings.ReplaceAll(probeError(err), key, "<redacted>")
		}
		sample = err == nil || unhealthy(err)
	}
	if ctx.Err() != nil {
		return
	}
	at := now()
	r.CheckedAt = &at
	if sample {
		p.Set.Record(l.Provider, l.Model, !r.OK)
	}
	p.mu.Lock()
	if p.last == nil {
		p.last = map[Key]ProbeResult{}
	}
	p.last[Key{l.Provider, l.Model}] = r
	p.mu.Unlock()
}

func apiKind(l capacity.Limit) string {
	switch {
	case l.Provider == "anthropic" || l.Provider == "openai" || l.Provider == "deepseek":
		return l.Provider
	case l.BaseURL != "":
		return "openai"
	}
	return ""
}

func complete(ctx context.Context, kind string, l capacity.Limit, key string, c *http.Client, timeout time.Duration) error {
	prov, err := llm.New(llm.Config{Provider: kind, Model: l.Model, BaseURL: l.BaseURL, APIKey: key, HTTPClient: c, Timeout: timeout})
	if err != nil {
		return err
	}
	_, err = prov.Complete(ctx, llm.Request{Messages: []llm.Message{{Role: "user", Content: "ping"}}, MaxTokens: 1})
	return err
}

func unhealthy(err error) bool {
	var ae *llm.APIError
	if errors.As(err, &ae) {
		return ae.Status == http.StatusTooManyRequests || ae.Status == http.StatusRequestTimeout || ae.Status >= 500
	}
	return true
}

func probeError(err error) string {
	var ae *llm.APIError
	if errors.As(err, &ae) {
		return fmt.Sprintf("status %d", ae.Status)
	}
	return err.Error()
}

package server

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/usage"
)

const (
	envUsagePrepaidUSD = "SUBSPACE_USAGE_CREDITS_USD"
	envUsageEURToUSD   = "SUBSPACE_USAGE_EUR_TO_USD"
	envUsageOpenCode   = "SUBSPACE_USAGE_OPENCODE"
	envUsageClaude     = "SUBSPACE_USAGE_CLAUDE"
	envUsageCodex      = "SUBSPACE_USAGE_CODEX"
	envUsageInterval   = "SUBSPACE_USAGE_INTERVAL"
)

type usageSetup struct {
	usage.Config
	Interval   time.Duration
	Collectors []usage.Collector
}

func usageConfigFromEnv(env func(string) string) (usageSetup, error) {
	s := usageSetup{Config: usage.Config{EURToUSD: 1}, Interval: time.Minute}
	num := func(key string, dst *float64, positive bool) error {
		v := env(key)
		if v == "" {
			return nil
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || (positive && f == 0) {
			return fmt.Errorf("%s: invalid value %q", key, v)
		}
		*dst = f
		return nil
	}
	if err := num(envUsagePrepaidUSD, &s.TotalCreditsUSD, false); err != nil {
		return s, err
	}
	if err := num(envUsageEURToUSD, &s.EURToUSD, true); err != nil {
		return s, err
	}
	if v := env(envUsageInterval); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second {
			return s, fmt.Errorf("%s: invalid duration %q (minimum 1s)", envUsageInterval, v)
		}
		s.Interval = d
	}
	for _, src := range []struct {
		key  string
		make func(agent, path string) usage.Collector
	}{
		{envUsageOpenCode, func(a, p string) usage.Collector { return &usage.OpenCode{Path: p, AgentID: a} }},
		{envUsageClaude, func(a, p string) usage.Collector { return &usage.ClaudeCode{Root: p, AgentID: a} }},
		{envUsageCodex, func(a, p string) usage.Collector { return &usage.Codex{Root: p, AgentID: a} }},
	} {
		for _, e := range strings.Split(env(src.key), ",") {
			e = strings.TrimSpace(e)
			if e == "" {
				continue
			}
			agent, path := "", e
			if a, p, ok := strings.Cut(e, "="); ok && !strings.ContainsAny(a, `/\`) {
				agent, path = strings.TrimSpace(a), strings.TrimSpace(p)
			}
			if path == "" {
				return s, fmt.Errorf("%s: empty path in %q", src.key, e)
			}
			s.Collectors = append(s.Collectors, src.make(agent, path))
		}
	}
	return s, nil
}

func mountUsage(d Deps, svc *usage.Service) {
	h := usage.NewHandler(svc)
	for _, p := range []string{"summary", "series", "providers", "capacity"} {
		d.API.Mount(d.Mux, "GET /api/v1/usage/"+p, h)
	}
	if cs := d.UsageSetup.Collectors; len(cs) > 0 {
		go svc.Run(d.Ctx, d.UsageSetup.Interval, cs...)
	}
}

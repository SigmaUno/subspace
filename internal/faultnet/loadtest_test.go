package faultnet_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/faultnet"
	"github.com/SigmaUno/subspace/internal/loadtest"
)

func TestLoadtestScenariosThroughFaultyNetwork(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	ctx := context.Background()
	srv, err := loadtest.StartInProcess(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	cases := []struct {
		cfg    loadtest.Config
		faults faultnet.Faults
	}{
		{loadtest.Config{Agents: 10, SSESubs: 5, Heartbeats: 2, Duration: 3 * time.Second},
			faultnet.Faults{Latency: 2 * time.Millisecond, Jitter: 3 * time.Millisecond, Bandwidth: 4 << 20, FirstByte: time.Millisecond}},
		{loadtest.Config{Scenario: loadtest.ScenarioContention, Agents: 20, Duration: 10 * time.Second},
			faultnet.Faults{Latency: time.Millisecond, Jitter: 5 * time.Millisecond, AcceptStall: 5 * time.Millisecond}},
		{loadtest.Config{Scenario: loadtest.ScenarioReconnectStorm, Agents: 5, SSESubs: 30, Duration: 3 * time.Second},
			faultnet.Faults{Latency: time.Millisecond, Jitter: time.Millisecond, FirstByte: 2 * time.Millisecond}},
	}
	for i, c := range cases {
		name := c.cfg.Scenario
		if name == "" {
			name = "steady"
		}
		t.Run(name, func(t *testing.T) {
			p, err := faultnet.Listen(strings.TrimPrefix(srv.URL, "http://"), uint64(100+i), c.faults)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = p.Close() }()
			cfg := c.cfg
			cfg.URL, cfg.OwnerToken = p.URL(), srv.OwnerToken
			cfg.Drain, cfg.Think = 10*time.Second, 5*time.Millisecond
			rep, err := loadtest.Run(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			rep.WriteText(&buf)
			if rep.Breached() || rep.Tasks.Claimed == 0 {
				t.Fatalf("%s\nfailed invariants %d, errors %v", buf.String(), rep.InvariantsFailed(), rep.ErrorSamples)
			}
			if st := p.Stats(); st.Conns == 0 || st.BytesDown == 0 {
				t.Fatalf("traffic bypassed the proxy: %+v", st)
			}
		})
	}
}

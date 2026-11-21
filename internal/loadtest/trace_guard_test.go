package loadtest

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/trace"
)

func TestTracingOverheadGuard(t *testing.T) {
	if os.Getenv("SUBSPACE_TRACE_GUARD") == "" {
		t.Skip("set SUBSPACE_TRACE_GUARD=1 (make loadtest-trace-guard) to compare throughput with tracing off and on at 1% sampling")
	}
	rounds := 3
	if v, err := strconv.Atoi(os.Getenv("SUBSPACE_TRACE_GUARD_ROUNDS")); err == nil && v > 0 {
		rounds = v
	}
	col := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	defer col.Close()

	measure := func(on bool) float64 {
		if on {
			t.Setenv(trace.EnvEndpoint, col.URL)
			t.Setenv(trace.EnvAllowPrivate, "true")
			t.Setenv(trace.EnvSample, "0.01")
		} else {
			t.Setenv(trace.EnvEndpoint, "")
		}
		rep, err := Run(context.Background(), Config{Agents: 50, Duration: 20 * time.Second, SSESubs: 100, Think: 50 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		if rep.ErrorRate != 0 {
			t.Fatalf("error rate %.4f with tracing %v", rep.ErrorRate, on)
		}
		return rep.Throughput
	}

	var off, on []float64
	for range rounds {
		off = append(off, measure(false))
		on = append(on, measure(true))
	}
	base, traced := median(off), median(on)
	t.Logf("throughput off %v (median %.1f rps), on at 1%% %v (median %.1f rps)", off, base, on, traced)
	if traced < base*0.95 {
		t.Fatalf("tracing at 1%% sampling costs %.1f%% throughput (limit 5%%)", (1-traced/base)*100)
	}
}

func median(v []float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	return s[len(s)/2]
}

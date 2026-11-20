package metrics

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type parsed struct {
	help    map[string]string
	typ     map[string]string
	samples map[string]float64
}

func parseText(t *testing.T, text string) parsed {
	t.Helper()
	p := parsed{help: map[string]string{}, typ: map[string]string{}, samples: map[string]float64{}}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "# HELP "):
			name, help, _ := strings.Cut(strings.TrimPrefix(line, "# HELP "), " ")
			p.help[name] = help
		case strings.HasPrefix(line, "# TYPE "):
			name, typ, _ := strings.Cut(strings.TrimPrefix(line, "# TYPE "), " ")
			p.typ[name] = typ
		case line == "" || strings.HasPrefix(line, "#"):
			t.Fatalf("unexpected line %q", line)
		default:
			series, val := splitSample(t, line)
			if _, dup := p.samples[series]; dup {
				t.Fatalf("duplicate series %s", series)
			}
			v, err := strconv.ParseFloat(val, 64)
			if err != nil {
				t.Fatalf("bad value in %q: %v", line, err)
			}
			p.samples[series] = v
		}
	}
	return p
}

func splitSample(t *testing.T, line string) (string, string) {
	t.Helper()
	end := strings.LastIndexByte(line, ' ')
	if end < 0 {
		t.Fatalf("malformed sample %q", line)
	}
	series, val := line[:end], line[end+1:]
	brace := strings.IndexByte(series, '{')
	if brace < 0 {
		return series, val
	}
	if !strings.HasSuffix(series, "}") {
		t.Fatalf("unterminated labels in %q", line)
	}
	body := series[brace+1 : len(series)-1]
	var pairs []string
	for body != "" {
		name, rest, ok := strings.Cut(body, `="`)
		if !ok {
			t.Fatalf("bad label in %q", line)
		}
		var b strings.Builder
		i := 0
		for ; i < len(rest) && rest[i] != '"'; i++ {
			if rest[i] == '\\' {
				i++
				switch rest[i] {
				case 'n':
					b.WriteByte('\n')
				default:
					b.WriteByte(rest[i])
				}
				continue
			}
			b.WriteByte(rest[i])
		}
		if i >= len(rest) {
			t.Fatalf("unterminated label value in %q", line)
		}
		pairs = append(pairs, name+"="+strconv.Quote(b.String()))
		body = strings.TrimPrefix(rest[i+1:], ",")
	}
	sort.Strings(pairs)
	return series[:brace] + "{" + strings.Join(pairs, ",") + "}", val
}

func scrape(t *testing.T, r *Registry) string {
	t.Helper()
	var buf bytes.Buffer
	if err := r.WriteText(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestExpositionRoundTrip(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounterVec("t_requests_total", "Requests.\nSecond line.", "route", "code")
	g := r.NewGaugeVec("t_depth", "Depth.")
	h := r.NewHistogramVec("t_latency_seconds", "Latency.", []float64{0.1, 1, 10}, "tool")

	nasty := "a\"b\\c\nd"
	c.With("GET /x/{id}", "200").Add(3)
	c.With(nasty, "500").Inc()
	g.With().Set(-2.5)
	for _, v := range []float64{0.05, 0.1, 0.5, 5, 50} {
		h.With("claim_task").Observe(v)
	}
	h.With("empty")

	p := parseText(t, scrape(t, r))

	if p.typ["t_requests_total"] != "counter" || p.typ["t_depth"] != "gauge" || p.typ["t_latency_seconds"] != "histogram" {
		t.Fatalf("types: %v", p.typ)
	}
	if p.help["t_requests_total"] != `Requests.\nSecond line.` {
		t.Fatalf("help: %q", p.help["t_requests_total"])
	}
	want := map[string]float64{
		`t_requests_total{code="200",route="GET /x/{id}"}`:                3,
		`t_requests_total{code="500",route=` + strconv.Quote(nasty) + `}`: 1,
		`t_depth`: -2.5,
		`t_latency_seconds_bucket{le="0.1",tool="claim_task"}`:  2,
		`t_latency_seconds_bucket{le="1",tool="claim_task"}`:    3,
		`t_latency_seconds_bucket{le="10",tool="claim_task"}`:   4,
		`t_latency_seconds_bucket{le="+Inf",tool="claim_task"}`: 5,
		`t_latency_seconds_count{tool="claim_task"}`:            5,
		`t_latency_seconds_bucket{le="0.1",tool="empty"}`:       0,
		`t_latency_seconds_bucket{le="1",tool="empty"}`:         0,
		`t_latency_seconds_bucket{le="10",tool="empty"}`:        0,
		`t_latency_seconds_bucket{le="+Inf",tool="empty"}`:      0,
		`t_latency_seconds_count{tool="empty"}`:                 0,
		`t_latency_seconds_sum{tool="empty"}`:                   0,
	}
	for k, v := range want {
		if got, ok := p.samples[k]; !ok || got != v {
			t.Errorf("%s = %v (present %v), want %v", k, got, ok, v)
		}
	}
	if got := p.samples[`t_latency_seconds_sum{tool="claim_task"}`]; math.Abs(got-55.65) > 1e-9 {
		t.Errorf("sum = %v, want 55.65", got)
	}
	if len(p.samples) != len(want)+1 {
		t.Errorf("got %d samples, want %d", len(p.samples), len(want)+1)
	}
}

func TestSeriesCapFoldsIntoOverflow(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounterVec("t_total", "T.", "tool")
	c.v.max = 3
	for i := range 10 {
		c.With(fmt.Sprintf("tool-%d", i)).Inc()
	}
	p := parseText(t, scrape(t, r))
	if len(p.samples) != 4 {
		t.Fatalf("series = %d, want 3 + overflow: %v", len(p.samples), p.samples)
	}
	if got := p.samples[`t_total{tool="other"}`]; got != 7 {
		t.Fatalf("overflow = %v, want 7", got)
	}
}

func TestConcurrentUpdatesAndScrapes(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounterVec("t_total", "T.", "tool", "role")
	g := r.NewGaugeVec("t_gauge", "G.")
	h := r.NewHistogramVec("t_seconds", "H.", latencyBuckets, "tool")
	r.OnScrape(func(context.Context) { g.With().Add(1) })

	const workers, perWorker = 16, 2000
	tools := []string{"claim_task", "heartbeat", "submit", "review"}
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perWorker {
				tool := tools[(w+i)%len(tools)]
				c.With(tool, "worker").Inc()
				h.With(tool).Observe(float64(i%100) / 1000)
			}
		}()
	}
	stop := make(chan struct{})
	var scrapes sync.WaitGroup
	scrapes.Add(1)
	go func() {
		defer scrapes.Done()
		for {
			select {
			case <-stop:
				return
			default:
				parseText(t, scrape(t, r))
			}
		}
	}()
	wg.Wait()
	close(stop)
	scrapes.Wait()

	p := parseText(t, scrape(t, r))
	var total, count float64
	for _, tool := range tools {
		total += p.samples[fmt.Sprintf(`t_total{role="worker",tool=%q}`, tool)]
		count += p.samples[fmt.Sprintf(`t_seconds_count{tool=%q}`, tool)]
		if inf := p.samples[fmt.Sprintf(`t_seconds_bucket{le="+Inf",tool=%q}`, tool)]; inf != p.samples[fmt.Sprintf(`t_seconds_count{tool=%q}`, tool)] {
			t.Errorf("%s: +Inf bucket %v != count", tool, inf)
		}
	}
	if total != workers*perWorker || count != workers*perWorker {
		t.Fatalf("counter %v histogram %v, want %d", total, count, workers*perWorker)
	}
}

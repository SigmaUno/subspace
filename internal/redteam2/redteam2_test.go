package redteam2

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	opsPerSeed    = 400
	globalEvery   = 3 * time.Second
	minimizeTries = 120
)

type outcome struct {
	v    *violation
	step int
}

func (e *env) run(ops []Op, deadline time.Time, gen *gen) ([]Op, outcome) {
	var done []Op
	report := func(v *violation) bool {
		if v == nil {
			return false
		}
		if e.collect == nil {
			return true
		}
		e.collect(v)
		return false
	}
	lastGlobal := time.Now()
	for i := 0; ; i++ {
		var op Op
		switch {
		case gen != nil:
			if i >= opsPerSeed || time.Now().After(deadline) {
				return done, outcome{v: e.checkGlobal(), step: i}
			}
			op = gen.next()
		case i < len(ops):
			op = ops[i]
		default:
			return done, outcome{v: e.checkGlobal(), step: i}
		}
		done = append(done, op)
		req, code, resp := e.exec(op)
		if v := e.checkCall(op, req, code, resp); report(v) {
			return done, outcome{v: v, step: i}
		}
		if gen != nil && time.Since(lastGlobal) > globalEvery {
			if v := e.checkGlobal(); report(v) {
				return done, outcome{v: v, step: i}
			}
			lastGlobal = time.Now()
		}
	}
}

func replay(t *testing.T, ops []Op) outcome {
	e := newEnv(t)
	defer e.close()
	_, o := e.run(ops, time.Time{}, nil)
	return o
}

func minimize(t *testing.T, ops []Op, inv string, until time.Time) []Op {
	tries := 0
	fails := func(c []Op) bool {
		tries++
		o := replay(t, c)
		return o.v != nil && o.v.inv == inv
	}
	if !fails(ops) {
		return ops
	}
	for n := 2; len(ops) >= 2 && tries < minimizeTries && time.Now().Before(until); {
		chunk := (len(ops) + n - 1) / n
		reduced := false
		for i := 0; i < len(ops) && tries < minimizeTries; i += chunk {
			c := append(append([]Op{}, ops[:i]...), ops[min(len(ops), i+chunk):]...)
			if len(c) > 0 && fails(c) {
				ops, n, reduced = c, max(n-1, 2), true
				break
			}
		}
		if !reduced {
			if n >= len(ops) {
				break
			}
			n = min(len(ops), n*2)
		}
	}
	return ops
}

func duration(t *testing.T) time.Duration {
	if s := os.Getenv("SUBSPACE_REDTEAM2_DURATION"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			t.Fatalf("SUBSPACE_REDTEAM2_DURATION: %v", err)
		}
		return d
	}
	if testing.Short() {
		return 3 * time.Second
	}
	return 20 * time.Second
}

func firstSeed(t *testing.T) int64 {
	s := os.Getenv("SUBSPACE_REDTEAM2_SEED")
	if s == "" {
		return time.Now().UnixNano() % 1_000_000_000
	}
	seed, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("SUBSPACE_REDTEAM2_SEED: %v", err)
	}
	return seed
}

func TestRedTeam(t *testing.T) {
	d := duration(t)
	seed := firstSeed(t)
	deadline := time.Now().Add(d)
	total := 0
	for s := seed; time.Now().Before(deadline); s++ {
		e := newEnv(t)
		g := &gen{r: rand.New(rand.NewSource(s)), e: e}
		ops, o := e.run(nil, deadline, g)
		e.close()
		total += len(ops)
		if o.v == nil {
			continue
		}
		small := minimize(t, ops[:min(len(ops), o.step+1)], o.v.inv, time.Now().Add(5*time.Minute))
		path := save(t, o.v, small)
		t.Fatalf("seed %d step %d: %s\nminimized to %d ops in %s:\n%s", s, o.step, o.v, len(small), path, trace(small))
	}
	t.Logf("%d ops from seed %d in %s", total, seed, d)
}

func trace(ops []Op) string {
	var b strings.Builder
	for i, op := range ops {
		fmt.Fprintf(&b, "%3d %s\n", i, op)
	}
	return b.String()
}

type regression struct {
	Invariant string `json:"invariant"`
	Detail    string `json:"detail"`
	Ops       []Op   `json:"ops"`
}

func save(t *testing.T, v *violation, ops []Op) string {
	dir := os.Getenv("SUBSPACE_REDTEAM2_OUT")
	if dir == "" {
		dir = t.TempDir()
	}
	b, _ := json.MarshalIndent(regression{Invariant: v.inv, Detail: v.detail, Ops: ops}, "", "  ")
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.json", v.inv, time.Now().UnixNano()))
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Logf("save: %v", err)
	}
	return path
}

func TestRegressions(t *testing.T) {
	files, err := filepath.Glob("testdata/regressions/*.json")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	for _, f := range files {
		t.Run(strings.TrimSuffix(filepath.Base(f), ".json"), func(t *testing.T) {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var r regression
			if err := json.Unmarshal(b, &r); err != nil {
				t.Fatal(err)
			}
			if o := replay(t, r.Ops); o.v != nil {
				t.Fatalf("step %d: %s\n%s", o.step, o.v, trace(r.Ops))
			}
		})
	}
}

func TestRedTeamSurvey(t *testing.T) {
	if os.Getenv("SUBSPACE_REDTEAM2_SURVEY") == "" {
		t.Skip("set SUBSPACE_REDTEAM2_SURVEY=1")
	}
	deadline := time.Now().Add(duration(t))
	seen := map[string]bool{}
	for s := firstSeed(t); time.Now().Before(deadline); s++ {
		e := newEnv(t)
		e.collect = func(v *violation) {
			key := v.inv + " " + v.detail
			if i := strings.Index(key, " returned "); i > 0 {
				key = key[:min(len(key), i)]
			}
			if len(key) > 60 {
				key = key[:60]
			}
			if seen[key] {
				return
			}
			seen[key] = true
			t.Logf("seed %d: %s", s, v)
		}
		if _, o := e.run(nil, deadline, &gen{r: rand.New(rand.NewSource(s)), e: e}); o.v != nil {
			e.collect(o.v)
		}
		e.close()
	}
}

package dst

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"
)

type Config struct {
	Seed   int64
	Steps  int
	Agents int
}

type Clock struct {
	mu sync.Mutex
	t  time.Time
}

func NewClock(t time.Time) *Clock { return &Clock{t: t} }

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type Kind int

const (
	opLeader Kind = iota
	opWorker
	opReviewer
	opOwner
	opTick
	opExpire
	opCrash
	opReplay
	opPause
	opRevoke
	opBurst
	opLoop
	opForbidden
)

var kindNames = [...]string{"leader", "worker", "reviewer", "owner", "tick", "expire", "crash", "replay", "pause", "revoke", "burst", "loop", "forbidden"}

var kindWeights = [...]int{22, 34, 12, 9, 8, 3, 3, 4, 2, 1, 4, 2, 1}

func (k Kind) String() string { return kindNames[k] }

type Op struct {
	Kind Kind
	R    uint64
}

const (
	planStream = 0x706c616e
	opStream   = 0x6f70
	setupSalt  = 0x7365747570
)

func Plan(seed int64, steps int) []Op {
	rng := rand.New(rand.NewPCG(uint64(seed), planStream)) //nolint:gosec
	total := 0
	for _, w := range kindWeights {
		total += w
	}
	ops := make([]Op, steps)
	for i := range ops {
		x, k := rng.IntN(total), 0
		for x >= kindWeights[k] {
			x -= kindWeights[k]
			k++
		}
		ops[i] = Op{Kind: Kind(k), R: rng.Uint64()}
	}
	return ops
}

type Violation struct {
	Step   int
	Kind   string
	Detail string
}

func (v *Violation) Error() string {
	return fmt.Sprintf("step %d: %s: %s", v.Step, v.Kind, v.Detail)
}

type Result struct {
	Config Config
	Ops    []Op
	Log    []string
	Err    error
}

func Run(ctx context.Context, cfg Config, ops []Op) *Result {
	res := &Result{Config: cfg, Ops: ops}
	s, err := start(ctx, cfg)
	if s != nil {
		defer s.close()
		res.Log = s.log
	}
	if err != nil {
		res.Err = err
		return res
	}
	res.Err = s.run(ops)
	res.Log = s.log
	return res
}

func Check(ctx context.Context, cfg Config) *Result {
	return Run(ctx, cfg, Plan(cfg.Seed, cfg.Steps))
}

func kindOf(err error) string {
	var v *Violation
	if errors.As(err, &v) {
		return v.Kind
	}
	return "error"
}

func failingPrefix(r *Result) []Op {
	var v *Violation
	if errors.As(r.Err, &v) && v.Step >= 0 && v.Step < len(r.Ops) {
		return r.Ops[:v.Step+1]
	}
	return r.Ops
}

func Shrink(ctx context.Context, failed *Result, budget int) *Result {
	kind := kindOf(failed.Err)
	best := failed
	try := func(ops []Op) bool {
		if budget <= 0 || ctx.Err() != nil {
			return false
		}
		budget--
		r := Run(ctx, failed.Config, ops)
		if r.Err == nil || kindOf(r.Err) != kind {
			return false
		}
		r.Ops = failingPrefix(r)
		best = r
		return true
	}
	if ops := failingPrefix(failed); len(ops) < len(failed.Ops) {
		try(append([]Op(nil), ops...))
	}
	n := 2
	for len(best.Ops) >= 2 && budget > 0 && ctx.Err() == nil {
		ops := best.Ops
		chunk := (len(ops) + n - 1) / n
		removed := false
		for i := 0; i < len(ops); i += chunk {
			cand := append(append([]Op(nil), ops[:i]...), ops[min(i+chunk, len(ops)):]...)
			if len(cand) > 0 && try(cand) {
				n = max(n-1, 2)
				removed = true
				break
			}
		}
		if !removed {
			if chunk == 1 {
				break
			}
			n = min(n*2, len(ops))
		}
	}
	return best
}

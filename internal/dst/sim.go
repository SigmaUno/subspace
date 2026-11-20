package dst

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

var Epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

type Step struct {
	Op  Op
	At  time.Time
	Err string
}

type Outcome struct {
	Seed      int64
	Steps     []Step
	Violation *Violation
}

func (o *Outcome) Failed() bool { return o.Violation != nil }

func (o *Outcome) Ops() []Op {
	ops := make([]Op, len(o.Steps))
	for i, s := range o.Steps {
		ops[i] = s.Op
	}
	return ops
}

func (o *Outcome) WriteTrace(w io.Writer) {
	for i, s := range o.Steps {
		line := fmt.Sprintf("%4d +%-9s %s", i, s.At.Sub(Epoch).Truncate(time.Millisecond), s.Op)
		if s.Err != "" {
			line += " -> " + s.Err
		}
		_, _ = fmt.Fprintln(w, line)
	}
	if o.Violation != nil {
		_, _ = fmt.Fprintf(w, "VIOLATION %s\n", o.Violation)
	}
}

type Runner struct {
	Factory Factory
	Dir     string
}

func (r *Runner) Run(ctx context.Context, seed int64, steps int) (*Outcome, error) {
	return r.exec(ctx, seed, steps, nil)
}

func (r *Runner) Replay(ctx context.Context, seed int64, ops []Op) (*Outcome, error) {
	return r.exec(ctx, seed, len(ops), ops)
}

func (r *Runner) exec(ctx context.Context, seed int64, steps int, ops []Op) (out *Outcome, err error) {
	dir, err := os.MkdirTemp(r.Dir, fmt.Sprintf("dst-%d-*", seed))
	if err != nil {
		return nil, err
	}
	defer func() {
		if rerr := os.RemoveAll(dir); err == nil && rerr != nil {
			err = rerr
		}
	}()
	w := NewWorld(seed)
	clock := NewClock(Epoch)
	sys, err := r.Factory(dir, clock, w.Lease)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := sys.Close(); err == nil && cerr != nil {
			err = cerr
		}
	}()
	setup, err := sys.Setup(ctx, w)
	if err != nil {
		return nil, fmt.Errorf("setup: %w", err)
	}
	gen := NewGen(seed, w, setup)
	chk := NewChecker()
	pre, err := sys.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	chk.audit = pre.Audit
	out = &Outcome{Seed: seed}
	for i := range steps {
		var op Op
		if ops != nil {
			op = ops[i]
		} else {
			op = gen.Next(pre)
		}
		res := sys.Do(ctx, op)
		post, err := sys.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		st := Step{Op: op, At: pre.Now}
		if res.Err != nil {
			st.Err = res.Err.Error()
		}
		out.Steps = append(out.Steps, st)
		if v := chk.Check(op, res, pre, post); v != nil {
			out.Violation = v
			return out, nil
		}
		pre = post
	}
	return out, nil
}

func (r *Runner) Shrink(ctx context.Context, o *Outcome, budget int) (*Outcome, error) {
	if !o.Failed() {
		return o, nil
	}
	best := o
	inv := o.Violation.Invariant
	for chunk := len(best.Steps) / 2; chunk >= 1 && budget > 0; {
		removed := false
		for start := 0; start+chunk <= len(best.Steps) && budget > 0; {
			ops := best.Ops()
			cand := append(append([]Op{}, ops[:start]...), ops[start+chunk:]...)
			budget--
			got, err := r.Replay(ctx, o.Seed, cand)
			if err != nil {
				return nil, err
			}
			if got.Failed() && got.Violation.Invariant == inv {
				best, removed = got, true
				continue
			}
			start += chunk
		}
		if !removed {
			chunk /= 2
		}
	}
	return best, nil
}

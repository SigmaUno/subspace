package dst

import (
	"context"
	"fmt"
	"io"
	"os"
	"reflect"
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
	Factory    Factory
	World      func(seed int64) World
	Dir        string
	Serial     bool
	Leaders    bool
	Subsystems bool
}

type Settler interface {
	Settle()
}

func ServerRunner(dir string) *Runner {
	return &Runner{Factory: NewServerSystem, World: NewServerWorld, Dir: dir}
}

func SubsystemsRunner(dir string) *Runner {
	r := ServerRunner(dir)
	r.Subsystems = true
	return r
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
	world := r.World
	if world == nil {
		world = NewWorld
	}
	w := world(seed)
	if r.Leaders {
		w.Rival = "lead2"
	}
	if r.Subsystems && w.Server != nil {
		w.Server.V4 = newV4World(seed, w)
	}
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
	gen := NewGen(seed, w, setup, r.Serial)
	chk := NewChecker(w.Lease)
	pre, err := sys.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	chk.Init(pre)
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
		v := chk.Check(op, res, pre, post)
		if v == nil && (op.Kind == OpRestart || i == steps-1) {
			v, err = verifyAudit(ctx, sys, post)
			if err != nil {
				return nil, err
			}
		}
		if v != nil {
			out.Violation = v
			return out, nil
		}
		pre = post
	}
	if st, ok := sys.(Settler); ok {
		st.Settle()
		post, err := sys.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		out.Violation = chk.Final(post)
	}
	return out, nil
}

func verifyAudit(ctx context.Context, sys System, s *State) (*Violation, error) {
	full, err := sys.Audit(ctx)
	if err != nil {
		return nil, err
	}
	if len(full) != len(s.Audit) {
		return violation(InvAudit, "audit log has %d events, %d observed", len(full), len(s.Audit)), nil
	}
	for i, e := range full {
		if !reflect.DeepEqual(e, s.Audit[i]) {
			return violation(InvAudit, "audit event %d changed after it was written", e.ID), nil
		}
	}
	return nil, nil
}

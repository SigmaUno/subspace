package modelcheck

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

type Divergence struct {
	Path     []Action
	Action   Action
	Retry    bool
	WantCode string
	GotCode  string
	Want     Obs
	Got      Obs
}

func (d *Divergence) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "board diverges from the reference model after %d steps:\n", len(d.Path))
	for i, a := range d.Path {
		fmt.Fprintf(&b, "  %2d. %s\n", i+1, a)
	}
	retry := ""
	if d.Retry {
		retry = " retried with the same idempotency key"
	}
	fmt.Fprintf(&b, "  >>  %s%s: error code model=%q board=%q\n", d.Action, retry, d.WantCode, d.GotCode)
	if d.Want.Paused != d.Got.Paused || d.Want.Closed != d.Got.Closed || d.Want.AgentPaused != d.Got.AgentPaused {
		fmt.Fprintf(&b, "  paused=%v closed=%v agents paused=%v; board paused=%v closed=%v agents paused=%v\n",
			d.Want.Paused, d.Want.Closed, d.Want.AgentPaused, d.Got.Paused, d.Got.Closed, d.Got.AgentPaused)
	}
	for i := range d.Want.Tasks {
		if d.Want.Tasks[i] != d.Got.Tasks[i] {
			fmt.Fprintf(&b, "  T%d: model=%+v board=%+v\n", i, d.Want.Tasks[i], d.Got.Tasks[i])
		}
	}
	return b.String()
}

func step(ctx context.Context, sys *System, s State, a Action, path []Action) (State, error) {
	next, want := s.Apply(a)
	got := sys.Do(ctx, s, a)
	obs, err := sys.Observe(ctx)
	if err != nil {
		return next, err
	}
	if got != want || obs != next.Observe() {
		return next, &Divergence{Path: slices.Clone(path), Action: a, WantCode: want, GotCode: got, Want: next.Observe(), Got: obs}
	}
	if !a.Kind.idempotent() || next == s {
		return next, nil
	}
	again, same, err := sys.Retry(ctx)
	if err != nil {
		return next, err
	}
	if obs, err = sys.Observe(ctx); err != nil {
		return next, err
	}
	if again != want || !same || obs != next.Observe() {
		if again == want && !same {
			again += " (different result body)"
		}
		return next, &Divergence{Path: slices.Clone(path), Action: a, Retry: true, WantCode: want, GotCode: again, Want: next.Observe(), Got: obs}
	}
	return next, nil
}

func (tp *Template) Run(ctx context.Context, path []Action) (State, error) {
	return tp.runOn(ctx, tp.Start, path)
}

func (tp *Template) RunFlapping(ctx context.Context, path []Action) (State, error) {
	return tp.runOn(ctx, tp.StartFlapping, path)
}

func (tp *Template) runOn(ctx context.Context, start func() (*System, error), path []Action) (State, error) {
	sys, err := start()
	if err != nil {
		return State{}, err
	}
	defer sys.Close()
	return run(ctx, sys, path)
}

func run(ctx context.Context, sys *System, path []Action) (State, error) {
	s := Initial(sys.flap)
	for i, a := range path {
		var err error
		if s, err = step(ctx, sys, s, a, path[:i]); err != nil {
			return s, err
		}
	}
	return s, nil
}

type Config struct {
	Root    []Action
	Depth   int
	Workers int
}

type Stats struct {
	Depth  int
	States int
	Edges  int
}

type node struct {
	state State
	path  []Action
	snap  *Snapshot
}

type expansion struct {
	children []node
	edges    int
	err      error
}

func Explore(ctx context.Context, tp *Template, cfg Config) (Stats, error) {
	workers := max(cfg.Workers, 1)
	systems := make([]*System, workers)
	defer func() {
		for _, s := range systems {
			if s != nil {
				s.Close()
			}
		}
	}()
	for i := range systems {
		var err error
		if systems[i], err = tp.Start(); err != nil {
			return Stats{}, err
		}
	}
	init, err := run(ctx, systems[0], cfg.Root)
	if err != nil {
		return Stats{}, err
	}
	root, err := systems[0].Snapshot(ctx)
	if err != nil {
		return Stats{}, err
	}
	alpha := Alphabet()
	visited := map[Key]struct{}{init.Key(): {}}
	level := []node{{state: init, path: slices.Clip(cfg.Root), snap: root}}
	st := Stats{States: 1}
	for d := 0; d < cfg.Depth && len(level) > 0; d++ {
		res := make([]expansion, len(level))
		var next atomic.Int64
		var stop atomic.Bool
		var wg sync.WaitGroup
		for _, sys := range systems[:min(workers, len(level))] {
			wg.Go(func() {
				for !stop.Load() && ctx.Err() == nil {
					i := int(next.Add(1) - 1)
					if i >= len(level) {
						return
					}
					res[i] = expand(ctx, sys, level[i], alpha, visited)
					if res[i].err != nil {
						stop.Store(true)
					}
				}
			})
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return st, err
		}
		var nextLevel []node
		for _, r := range res {
			if r.err != nil {
				return st, r.err
			}
			st.Edges += r.edges
			for _, c := range r.children {
				k := c.state.Key()
				if _, ok := visited[k]; ok {
					continue
				}
				visited[k] = struct{}{}
				nextLevel = append(nextLevel, c)
			}
		}
		st.Depth = d + 1
		st.States += len(nextLevel)
		level = nextLevel
	}
	return st, nil
}

func expand(ctx context.Context, sys *System, n node, alpha []Action, visited map[Key]struct{}) (out expansion) {
	if out.err = sys.Restore(ctx, n.snap); out.err != nil {
		return out
	}
	var changing, same []Action
	for _, a := range alpha {
		if next, _ := n.state.Apply(a); next != n.state {
			changing = append(changing, a)
		} else {
			same = append(same, a)
		}
	}
	if out.err = checkUnchanged(ctx, sys, n, same); out.err != nil {
		return out
	}
	out.edges += len(same)
	for _, a := range changing {
		if out.err = sys.Restore(ctx, n.snap); out.err != nil {
			return out
		}
		var next State
		if next, out.err = step(ctx, sys, n.state, a, n.path); out.err != nil {
			return out
		}
		out.edges++
		if _, ok := visited[next.Key()]; ok {
			continue
		}
		c := node{state: next, path: append(slices.Clip(n.path), a)}
		if c.snap, out.err = sys.Snapshot(ctx); out.err != nil {
			return out
		}
		out.children = append(out.children, c)
	}
	return out
}

func checkUnchanged(ctx context.Context, sys *System, n node, actions []Action) error {
	ok := true
	for _, a := range actions {
		_, want := n.state.Apply(a)
		if sys.Do(ctx, n.state, a) != want {
			ok = false
			break
		}
	}
	if ok {
		obs, err := sys.Observe(ctx)
		if err != nil {
			return err
		}
		if obs == n.state.Observe() {
			return nil
		}
	}
	for _, a := range actions {
		if err := sys.Restore(ctx, n.snap); err != nil {
			return err
		}
		if _, err := step(ctx, sys, n.state, a, n.path); err != nil {
			return err
		}
	}
	return fmt.Errorf("unchanged-state actions from %v diverge only in combination", n.path)
}

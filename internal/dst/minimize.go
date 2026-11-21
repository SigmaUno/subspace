package dst

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
)

type minimizer struct {
	r      *Runner
	seed   int64
	inv    string
	budget int
	best   *Outcome
}

func (r *Runner) Minimize(ctx context.Context, o *Outcome, budget int) (*Outcome, error) {
	if !o.Failed() {
		return o, nil
	}
	m := &minimizer{r: r, seed: o.Seed, inv: o.Violation.Invariant, budget: budget, best: o}
	for {
		n := len(m.best.Steps)
		if err := m.deleteChunks(ctx); err != nil {
			return nil, err
		}
		if err := m.deleteKinds(ctx); err != nil {
			return nil, err
		}
		if err := m.simplify(ctx); err != nil {
			return nil, err
		}
		if len(m.best.Steps) >= n || m.budget <= 0 {
			return m.best, nil
		}
	}
}

func (m *minimizer) try(ctx context.Context, ops []Op) (bool, error) {
	if m.budget <= 0 {
		return false, nil
	}
	m.budget--
	got, err := m.r.Replay(ctx, m.seed, ops)
	if err != nil {
		return false, err
	}
	if got.Failed() && got.Violation.Invariant == m.inv && len(got.Steps) <= len(ops) {
		m.best = got
		return true, nil
	}
	return false, nil
}

func (m *minimizer) deleteKinds(ctx context.Context) error {
	var kinds []Kind
	for _, op := range m.best.Ops() {
		if !slices.Contains(kinds, op.Kind) {
			kinds = append(kinds, op.Kind)
		}
	}
	for _, k := range kinds {
		if m.budget <= 0 {
			return nil
		}
		ops := slices.DeleteFunc(m.best.Ops(), func(op Op) bool { return op.Kind == k })
		if len(ops) == len(m.best.Steps) || len(ops) == 0 {
			continue
		}
		if _, err := m.try(ctx, ops); err != nil {
			return err
		}
	}
	return nil
}

func (m *minimizer) deleteChunks(ctx context.Context) error {
	for chunk := len(m.best.Steps) / 2; chunk >= 1 && m.budget > 0; {
		removed := false
		for start := 0; start+chunk <= len(m.best.Steps) && m.budget > 0; {
			ops := m.best.Ops()
			ok, err := m.try(ctx, slices.Concat(ops[:start], ops[start+chunk:]))
			if err != nil {
				return err
			}
			if ok {
				removed = true
				continue
			}
			start += chunk
		}
		if !removed {
			chunk /= 2
		}
	}
	return nil
}

func (m *minimizer) simplify(ctx context.Context) error {
	for i := 0; i < len(m.best.Steps) && m.budget > 0; i++ {
		ops := m.best.Ops()
		for _, alt := range alternatives(ops[i]) {
			ok, err := m.try(ctx, slices.Concat(ops[:i], alt, ops[i+1:]))
			if err != nil {
				return err
			}
			if ok {
				break
			}
		}
	}
	return nil
}

func alternatives(op Op) [][]Op {
	var out [][]Op
	switch op.Kind {
	case OpBatch:
		out = append(out, op.Sub)
		for j := range op.Sub {
			if len(op.Sub) > 2 {
				out = append(out, []Op{{Kind: OpBatch, Sub: slices.Delete(slices.Clone(op.Sub), j, j+1)}})
			}
		}
	case OpAdvance:
		if d := (op.Dur / 2).Truncate(time.Second); d != 0 {
			out = append(out, []Op{{Kind: OpAdvance, Dur: d}})
		}
	case OpRestart:
		if op.Crash {
			out = append(out, []Op{{Kind: OpRestart}})
		}
	case OpScheduleTick, OpSuggestSplit:
		if lo := map[Kind]int{OpScheduleTick: 1, OpSuggestSplit: 2}[op.Kind]; op.N > lo {
			alt := op
			alt.N = lo
			out = append(out, []Op{alt})
		}
	case OpIncidentStart, OpSetGates:
		parts := strings.Split(op.Text, ",")
		for j := range parts {
			if len(parts) > 1 {
				alt := op
				alt.Text = strings.Join(slices.Delete(slices.Clone(parts), j, j+1), ",")
				out = append(out, []Op{alt})
			}
		}
	case OpLogOutput:
		if op.N != 0 {
			a := op
			a.N = 0
			out = append(out, []Op{a})
		}
		if first, _, ok := strings.Cut(op.Text, "\n"); ok {
			a := op
			a.Text = first
			out = append(out, []Op{a})
		}
	case OpLeak:
		if op.Text != board.ChannelMessage {
			a := op
			a.Text, a.Task = board.ChannelMessage, ""
			out = append(out, []Op{a})
		}
	case OpSetQuota:
		if op.Cost != 0 {
			a := op
			a.Cost = 0
			out = append(out, []Op{a})
		}
		if op.N != 0 {
			a := op
			a.N = 0
			out = append(out, []Op{a})
		}
		if op.Limit != 0 {
			a := op
			a.Limit = 0
			out = append(out, []Op{a})
		}
	}
	return out
}

func WriteLog(w io.Writer, o *Outcome) error {
	enc := json.NewEncoder(w)
	for _, s := range o.Steps {
		if err := enc.Encode(s.Op); err != nil {
			return err
		}
	}
	return nil
}

func ReadLog(r io.Reader) ([]Op, error) {
	var ops []Op
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for n := 1; sc.Scan(); n++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var op Op
		if err := json.Unmarshal(sc.Bytes(), &op); err != nil {
			return nil, fmt.Errorf("step log line %d: %w", n, err)
		}
		ops = append(ops, op)
	}
	return ops, sc.Err()
}

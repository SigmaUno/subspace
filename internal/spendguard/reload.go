package spendguard

import "context"

func (g *Guard) Reload(ctx context.Context) error {
	g.evalMu.Lock()
	defer g.evalMu.Unlock()
	var l Limits
	if err := g.load(ctx, limitsKey, &l); err != nil {
		return err
	}
	var st state
	if err := g.load(ctx, stateKey, &st); err != nil {
		return err
	}
	st.init()
	g.mu.Lock()
	g.limits, g.st, g.dirty = l, st, false
	g.mu.Unlock()
	return nil
}

func (s *state) init() {
	if s.Seen == nil {
		s.Seen = map[string]float64{}
	}
	if s.Tripped == nil {
		s.Tripped = map[Window]string{}
	}
	if s.Warned == nil {
		s.Warned = map[Window]string{}
	}
	if s.WarnedAt == nil {
		s.WarnedAt = map[Window]int64{}
	}
}

package acceptance

import "sync"

type usageLine struct {
	agent, task string
	in, out     int64
	usd         float64
}

type ledger struct {
	mu    sync.Mutex
	lines []usageLine
}

func (l *ledger) add(u usageLine) {
	l.mu.Lock()
	l.lines = append(l.lines, u)
	l.mu.Unlock()
}

func (l *ledger) sum(match func(usageLine) bool) (in, out int64, usd float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, u := range l.lines {
		if match(u) {
			in += u.in
			out += u.out
			usd += u.usd
		}
	}
	return
}

package prsync

func (p *Poller) Tracked() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.last) + len(p.retries)
}

package faultnet

import (
	"math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type Faults struct {
	Latency     time.Duration
	Jitter      time.Duration
	Bandwidth   int
	ResetUp     float64
	ResetDown   float64
	HalfOpen    float64
	Truncate    float64
	FirstByte   time.Duration
	AcceptStall time.Duration
}

type Stats struct {
	Conns     int64
	Active    int64
	Resets    int64
	Truncated int64
	HalfOpen  int64
	BytesUp   int64
	BytesDown int64
}

type Proxy struct {
	ln     net.Listener
	target string
	seed   uint64
	faults atomic.Pointer[Faults]
	next   atomic.Uint64

	mu     sync.Mutex
	conns  map[*link]struct{}
	closed chan struct{}
	once   sync.Once
	wg     sync.WaitGroup

	conns64, active, resets, truncated, halfOpen, up, down atomic.Int64
}

const chunk = 1024

func Listen(target string, seed uint64, f Faults) (*Proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &Proxy{ln: ln, target: target, seed: seed, conns: map[*link]struct{}{}, closed: make(chan struct{})}
	p.faults.Store(&f)
	p.wg.Go(p.accept)
	return p, nil
}

func (p *Proxy) Addr() string { return p.ln.Addr().String() }

func (p *Proxy) URL() string { return "http://" + p.Addr() }

func (p *Proxy) Set(f Faults) { p.faults.Store(&f) }

func (p *Proxy) Faults() Faults { return *p.faults.Load() }

func (p *Proxy) Stats() Stats {
	return Stats{
		Conns: p.conns64.Load(), Active: p.active.Load(), Resets: p.resets.Load(), Truncated: p.truncated.Load(),
		HalfOpen: p.halfOpen.Load(), BytesUp: p.up.Load(), BytesDown: p.down.Load(),
	}
}

func (p *Proxy) ResetAll() int {
	p.mu.Lock()
	ls := make([]*link, 0, len(p.conns))
	for l := range p.conns {
		ls = append(ls, l)
	}
	p.mu.Unlock()
	for _, l := range ls {
		l.kill(true)
	}
	return len(ls)
}

func (p *Proxy) Close() error {
	var err error
	p.once.Do(func() {
		close(p.closed)
		err = p.ln.Close()
		p.ResetAll()
		p.wg.Wait()
	})
	return err
}

func (p *Proxy) accept() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		n := p.next.Add(1)
		p.conns64.Add(1)
		l := &link{p: p, client: c, done: make(chan struct{})}
		p.mu.Lock()
		select {
		case <-p.closed:
			p.mu.Unlock()
			_ = c.Close()
			return
		default:
		}
		p.conns[l] = struct{}{}
		p.mu.Unlock()
		p.active.Add(1)
		p.wg.Go(func() { l.run(n) })
	}
}

type link struct {
	p        *Proxy
	client   net.Conn
	upstream atomic.Pointer[net.Conn]
	done     chan struct{}
	once     sync.Once
}

func (l *link) kill(reset bool) {
	l.once.Do(func() {
		close(l.done)
		conns := []net.Conn{l.client}
		if u := l.upstream.Load(); u != nil {
			conns = append(conns, *u)
		}
		for _, c := range conns {
			if tc, ok := c.(*net.TCPConn); ok && reset {
				_ = tc.SetLinger(0)
			}
			_ = c.Close()
		}
		if reset {
			l.p.resets.Add(1)
		}
	})
}

func (l *link) wait(d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-l.done:
		return false
	}
}

func (l *link) run(n uint64) {
	defer func() {
		l.kill(false)
		l.p.mu.Lock()
		delete(l.p.conns, l)
		l.p.mu.Unlock()
		l.p.active.Add(-1)
	}()
	if !l.wait(l.p.faults.Load().AcceptStall) {
		return
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	u, err := d.Dial("tcp", l.p.target)
	if err != nil {
		return
	}
	l.upstream.Store(&u)
	select {
	case <-l.done:
		_ = u.Close()
		return
	default:
	}
	var wg sync.WaitGroup
	wg.Go(func() { l.pump(4*n, l.client, u, true) })
	wg.Go(func() { l.pump(4*n+1, u, l.client, false) })
	wg.Wait()
}

type segment struct {
	b   []byte
	due time.Time
	eof bool
}

func (l *link) pump(stream uint64, src, dst net.Conn, up bool) {
	rng := rand.New(rand.NewPCG(l.p.seed, stream))      //nolint:gosec // seeded fault injection
	jitter := rand.New(rand.NewPCG(l.p.seed, stream+2)) //nolint:gosec // seeded fault injection
	q := make(chan segment, 64)
	go func() {
		defer close(q)
		var last time.Time
		for {
			buf := make([]byte, chunk)
			n, err := src.Read(buf)
			if n > 0 {
				f := l.p.faults.Load()
				due := time.Now().Add(f.Latency)
				if f.Jitter > 0 {
					due = due.Add(time.Duration(jitter.Int64N(int64(f.Jitter) + 1)))
				}
				if due.Before(last) {
					due = last
				}
				last = due
				select {
				case q <- segment{b: buf[:n], due: due}:
				case <-l.done:
					return
				}
			}
			if err != nil {
				select {
				case q <- segment{eof: true, due: last}:
				case <-l.done:
				}
				return
			}
		}
	}()
	first := true
	for seg := range q {
		if !l.wait(time.Until(seg.due)) {
			return
		}
		if seg.eof {
			if tc, ok := dst.(*net.TCPConn); ok {
				_ = tc.CloseWrite()
				continue
			}
			l.kill(false)
			return
		}
		f := l.p.faults.Load()
		if first && !up && !l.wait(f.FirstByte) {
			return
		}
		first = false
		b := seg.b
		switch {
		case up && hit(rng, f.ResetUp):
			l.forward(dst, b[:rng.IntN(len(b)+1)], f, up)
			l.kill(true)
			return
		case !up && hit(rng, f.ResetDown):
			l.kill(true)
			return
		case !up && hit(rng, f.Truncate):
			l.forward(dst, b[:rng.IntN(len(b))], f, up)
			l.p.truncated.Add(1)
			l.kill(false)
			return
		case !up && hit(rng, f.HalfOpen):
			l.p.halfOpen.Add(1)
			<-l.done
			return
		}
		if !l.forward(dst, b, f, up) {
			l.kill(true)
			return
		}
	}
}

func (l *link) forward(dst net.Conn, b []byte, f *Faults, up bool) bool {
	for len(b) > 0 {
		n := len(b)
		if f.Bandwidth > 0 {
			n = min(n, max(f.Bandwidth/20, 1))
		}
		w, err := dst.Write(b[:n])
		if up {
			l.p.up.Add(int64(w))
		} else {
			l.p.down.Add(int64(w))
		}
		if err != nil {
			return false
		}
		b = b[n:]
		if f.Bandwidth > 0 && !l.wait(time.Duration(n)*time.Second/time.Duration(f.Bandwidth)) {
			return false
		}
	}
	return true
}

func hit(rng *rand.Rand, p float64) bool {
	return p > 0 && rng.Float64() < p
}

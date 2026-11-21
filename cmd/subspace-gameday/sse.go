package main

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

type Subscriber struct {
	Slow    bool
	hello   chan struct{}
	msgs    atomic.Int64
	lagged  atomic.Int64
	resync  atomic.Int64
	closed  atomic.Bool
	release chan struct{}
	once    sync.Once
}

func (s *Subscriber) Complete(want int64) bool {
	return s.msgs.Load() >= want || s.lagged.Load()+s.resync.Load() > 0
}

func (s *Subscriber) Release() { s.once.Do(func() { close(s.release) }) }

func Subscribe(ctx context.Context, base, token string, slow bool, wg *sync.WaitGroup) *Subscriber {
	s := &Subscriber{Slow: slow, hello: make(chan struct{}), release: make(chan struct{})}
	hc := &http.Client{Transport: &http.Transport{}}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer s.closed.Store(true)
		defer hc.CloseIdleConnections()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/events?topics=all", nil)
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := hc.Do(req)
		if err != nil {
			return
		}
		defer func() { _ = resp.Body.Close() }()
		br := bufio.NewReaderSize(resp.Body, 4096)
		var helloed bool
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			typ, ok := strings.CutPrefix(strings.TrimSpace(line), "event: ")
			if !ok {
				continue
			}
			switch typ {
			case "hello":
				if !helloed {
					helloed = true
					close(s.hello)
				}
				if slow {
					select {
					case <-s.release:
					case <-ctx.Done():
						return
					}
				}
			case "message.created":
				s.msgs.Add(1)
			case "lagged":
				s.lagged.Add(1)
			case "resync":
				s.resync.Add(1)
			}
		}
	}()
	return s
}

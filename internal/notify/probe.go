package notify

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const testTimeout = 15 * time.Second

type SinkResult struct {
	Sink  string `json:"sink"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func (s *Service) SendTest() []SinkResult {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	now := s.cfg.Clock.Now()
	n := Notification{
		ID:       "test:" + strconv.FormatInt(now.UnixNano(), 10),
		Kind:     "test",
		Title:    "[TEST] Subspace test notification",
		Text:     "This is a test message sent from the Subspace settings. No action is needed.",
		Priority: 3,
		TS:       now,
	}
	lanes := s.snapshot()
	results := make([]SinkResult, len(lanes))
	var wg sync.WaitGroup
	for i, l := range lanes {
		wg.Go(func() {
			r := SinkResult{Sink: l.sink.Name(), OK: true}
			if err := l.sink.Send(ctx, n); err != nil {
				r.OK = false
				r.Error = redact(err)
			}
			results[i] = r
		})
	}
	wg.Wait()
	return results
}

func redact(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

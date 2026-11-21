package spawner

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/netguard"
)

const (
	DefaultLogPoll  = 5 * time.Second
	DefaultLogRate  = 50.0
	DefaultLogBurst = 500

	logChunkBytes = 4000
	logLineBytes  = 2048
	logFlushEvery = time.Second
)

type LogSink func(agentID, stream, text string)

func (d *Docker) Follow(ctx context.Context, containerID string, since int64, emit func(stream, line string)) error {
	if d.stream == nil {
		return ErrNotImplemented
	}
	q := url.Values{"follow": {"1"}, "stdout": {"1"}, "stderr": {"1"}}
	if since > 0 {
		q.Set("since", strconv.FormatInt(since, 10))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, engineHost+"/containers/"+url.PathEscape(containerID)+"/logs?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := d.stream.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		data, _ := netguard.ReadBody(resp.Body, 4096)
		return engineFailure(resp.StatusCode, data, "")
	}
	return demux(resp.Body, emit)
}

func demux(r io.Reader, emit func(stream, line string)) error {
	br := bufio.NewReaderSize(r, 32<<10)
	out := &lineSplitter{stream: "stdout", emit: emit}
	errw := &lineSplitter{stream: "stderr", emit: emit}
	defer errw.flush()
	defer out.flush()
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		w := io.Discard
		switch hdr[0] {
		case 0, 1:
			w = out
		case 2:
			w = errw
		}
		if _, err := io.CopyN(w, br, int64(binary.BigEndian.Uint32(hdr[4:]))); err != nil {
			return err
		}
	}
}

type lineSplitter struct {
	stream string
	emit   func(stream, line string)
	buf    []byte
}

func (s *lineSplitter) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			break
		}
		s.emit(s.stream, strings.TrimSuffix(string(s.buf[:i]), "\r"))
		s.buf = s.buf[i+1:]
	}
	for len(s.buf) >= logLineBytes {
		s.emit(s.stream, string(s.buf[:logLineBytes]))
		s.buf = s.buf[logLineBytes:]
	}
	return len(p), nil
}

func (s *lineSplitter) flush() {
	if len(s.buf) > 0 {
		s.emit(s.stream, strings.TrimSuffix(string(s.buf), "\r"))
		s.buf = nil
	}
}

func (d *Docker) WatchLogs(ctx context.Context, sink LogSink) {
	if d.client == nil {
		return
	}
	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		active = map[string]bool{}
		since  = map[string]int64{}
	)
	defer wg.Wait()
	t := time.NewTicker(cmp.Or(d.cfg.LogPoll, DefaultLogPoll))
	defer t.Stop()
	for {
		if list, err := d.list(ctx, d.label("agent"), false); err == nil {
			mu.Lock()
			seen := map[string]bool{}
			for _, c := range list {
				seen[c.ID] = true
				if active[c.ID] || !agentIDRe.MatchString(c.AgentID) {
					continue
				}
				active[c.ID] = true
				from := since[c.ID]
				wg.Add(1)
				go func() {
					defer wg.Done()
					d.pump(ctx, c, from, sink)
					mu.Lock()
					delete(active, c.ID)
					since[c.ID] = d.cfg.Now().Unix()
					mu.Unlock()
				}()
			}
			for id := range since {
				if !seen[id] && !active[id] {
					delete(since, id)
				}
			}
			mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (d *Docker) pump(ctx context.Context, c Container, since int64, sink LogSink) {
	b := &logBatch{
		agent: c.AgentID, sink: sink, now: d.cfg.Now,
		rate: cmp.Or(d.cfg.LogRate, DefaultLogRate), burst: float64(cmp.Or(d.cfg.LogBurst, DefaultLogBurst)),
	}
	b.tokens, b.last = b.burst, b.now()
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(logFlushEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				b.flush()
			}
		}
	}()
	_ = d.Follow(ctx, c.ID, since, b.add)
	close(done)
	b.flush()
}

type logBatch struct {
	mu      sync.Mutex
	agent   string
	sink    LogSink
	now     func() time.Time
	rate    float64
	burst   float64
	tokens  float64
	last    time.Time
	stream  string
	buf     strings.Builder
	dropped int
}

func (b *logBatch) allow() bool {
	now := b.now()
	b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (b *logBatch) add(stream, line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.allow() {
		b.dropped++
		return
	}
	if b.buf.Len() > 0 && (stream != b.stream || b.buf.Len()+len(line)+1 > logChunkBytes) {
		b.flushLocked()
	}
	b.stream = stream
	if b.buf.Len() > 0 {
		b.buf.WriteByte('\n')
	}
	b.buf.WriteString(line)
}

func (b *logBatch) flush() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flushLocked()
}

func (b *logBatch) flushLocked() {
	if b.buf.Len() > 0 {
		b.sink(b.agent, b.stream, b.buf.String())
		b.buf.Reset()
	}
	if b.dropped > 0 {
		b.sink(b.agent, "note", fmt.Sprintf("[subspace: %d log lines dropped by rate limit]", b.dropped))
		b.dropped = 0
	}
}

package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Event struct {
	ID   string
	Name string
	Data string
}

type Envelope struct {
	Type string          `json:"type"`
	TS   time.Time       `json:"ts"`
	Data json.RawMessage `json:"data"`
}

func (e Event) Envelope() (Envelope, error) {
	var env Envelope
	err := json.Unmarshal([]byte(e.Data), &env)
	return env, err
}

type RawEvent struct {
	ID   []byte
	Name []byte
	Data []byte
}

type eventReader struct {
	br      *bufio.Reader
	raw     RawEvent
	hasData bool
}

func newEventReader(r io.Reader) *eventReader {
	return &eventReader{br: bufio.NewReaderSize(r, 64<<10)}
}

func (r *eventReader) line() ([]byte, error) {
	l, err := r.br.ReadSlice('\n')
	if !errors.Is(err, bufio.ErrBufferFull) {
		return l, err
	}
	buf := append([]byte(nil), l...)
	for errors.Is(err, bufio.ErrBufferFull) {
		l, err = r.br.ReadSlice('\n')
		buf = append(buf, l...)
	}
	return buf, err
}

func (r *eventReader) reset() {
	r.raw = RawEvent{ID: r.raw.ID[:0], Name: r.raw.Name[:0], Data: r.raw.Data[:0]}
	r.hasData = false
}

func (r *eventReader) nextRaw() (RawEvent, error) {
	r.reset()
	for {
		line, err := r.line()
		if err != nil {
			return RawEvent{}, err
		}
		line = bytes.TrimRight(line, "\r\n")
		switch {
		case len(line) == 0:
			if r.hasData || len(r.raw.Name) > 0 {
				return r.raw, nil
			}
			r.reset()
		case line[0] == ':':
		default:
			k, v, _ := bytes.Cut(line, []byte(":"))
			v = bytes.TrimPrefix(v, []byte(" "))
			switch string(k) {
			case "id":
				r.raw.ID = append(r.raw.ID[:0], v...)
			case "event":
				r.raw.Name = append(r.raw.Name[:0], v...)
			case "data":
				if r.hasData {
					r.raw.Data = append(r.raw.Data, '\n')
				}
				r.raw.Data = append(r.raw.Data, v...)
				r.hasData = true
			}
		}
	}
}

func (r *eventReader) next() (Event, error) {
	raw, err := r.nextRaw()
	if err != nil {
		return Event{}, err
	}
	return Event{ID: string(raw.ID), Name: string(raw.Name), Data: string(raw.Data)}, nil
}

type EventStream struct {
	body io.ReadCloser
	r    *eventReader
}

func (s *EventStream) Next() (Event, error) { return s.r.next() }

func (s *EventStream) NextRaw() (RawEvent, error) { return s.r.nextRaw() }

func (s *EventStream) Close() error { return s.body.Close() }

func (o *Owner) Events(ctx context.Context, topics ...string) (*EventStream, error) {
	return o.EventsFrom(ctx, "", topics...)
}

func (o *Owner) EventsFrom(ctx context.Context, lastEventID string, topics ...string) (*EventStream, error) {
	path := "/api/v1/events"
	if len(topics) > 0 {
		path += "?" + url.Values{"topics": {strings.Join(topics, ",")}}.Encode()
	}
	req, err := newRequest(ctx, http.MethodGet, o.base+path, o.token, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	hc := *o.cfg.hc
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := readBody(resp)
		_ = resp.Body.Close()
		return nil, httpError(resp.StatusCode, b)
	}
	return &EventStream{body: resp.Body, r: newEventReader(resp.Body)}, nil
}

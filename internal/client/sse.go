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

type eventReader struct{ br *bufio.Reader }

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

func (r *eventReader) next() (Event, error) {
	var cur Event
	var data []string
	for {
		line, err := r.line()
		if err != nil {
			return Event{}, err
		}
		line = bytes.TrimRight(line, "\r\n")
		switch {
		case len(line) == 0:
			if len(data) > 0 || cur.Name != "" {
				cur.Data = strings.Join(data, "\n")
				return cur, nil
			}
			cur, data = Event{}, data[:0]
		case line[0] == ':':
		default:
			k, v, _ := bytes.Cut(line, []byte(":"))
			v = bytes.TrimPrefix(v, []byte(" "))
			switch string(k) {
			case "id":
				cur.ID = string(v)
			case "event":
				cur.Name = string(v)
			case "data":
				data = append(data, string(v))
			}
		}
	}
}

type EventStream struct {
	body io.ReadCloser
	r    *eventReader
}

func (s *EventStream) Next() (Event, error) { return s.r.next() }

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

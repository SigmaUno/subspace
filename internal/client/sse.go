package client

import (
	"bufio"
	"context"
	"encoding/json"
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

func (r *eventReader) next() (Event, error) {
	var cur Event
	var data []string
	for {
		line, err := r.br.ReadString('\n')
		if err != nil {
			return Event{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if len(data) > 0 || cur.Name != "" {
				cur.Data = strings.Join(data, "\n")
				return cur, nil
			}
			cur, data = Event{}, data[:0]
		case strings.HasPrefix(line, ":"):
		default:
			k, v, _ := strings.Cut(line, ":")
			v = strings.TrimPrefix(v, " ")
			switch k {
			case "id":
				cur.ID = v
			case "event":
				cur.Name = v
			case "data":
				data = append(data, v)
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
	path := "/api/v1/events"
	if len(topics) > 0 {
		path += "?" + url.Values{"topics": {strings.Join(topics, ",")}}.Encode()
	}
	req, err := newRequest(ctx, http.MethodGet, o.base+path, o.token, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
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

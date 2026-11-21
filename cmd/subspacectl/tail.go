package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	tailBacklog   = 50
	tailMaxLines  = 2000
	tailReconnect = time.Second
)

type sseEvent struct {
	name string
	data string
}

func (c *conn) stream(ctx context.Context, req request, handle func(sseEvent) error) error {
	httpReq, err := c.httpRequest(ctx, req, "text/event-stream")
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(httpReq) //nolint:gosec
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &cliError{exit: exitUnavailable, code: "unavailable", msg: c.redact("contact Subspace: " + err.Error())}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return c.apiError(resp.StatusCode, payload)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	var event sseEvent
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if event.name != "" || len(data) > 0 {
				event.data = strings.Join(data, "\n")
				if err := handle(event); err != nil {
					return err
				}
			}
			event, data = sseEvent{}, nil
		case strings.HasPrefix(line, ":"):
		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				event.name = value
			case "data":
				data = append(data, value)
			}
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return &cliError{exit: exitUnavailable, code: "unavailable", msg: c.redact("read Subspace stream: " + err.Error())}
	}
	return nil
}

type outputLine struct {
	Seq     int64   `json:"seq"`
	AgentID string  `json:"agent_id"`
	TaskID  *string `json:"task_id"`
	Stream  string  `json:"stream"`
	Text    string  `json:"text"`
	TS      string  `json:"ts"`
}

type tailer struct {
	c      *conn
	p      *printer
	task   string
	lines  int
	prefix bool
	last   map[string]int64
}

func (t *tailer) emit(l outputLine) error {
	if seen, ok := t.last[l.AgentID]; ok && l.Seq <= seen {
		return nil
	}
	t.last[l.AgentID] = l.Seq
	if t.task != "" && (l.TaskID == nil || *l.TaskID != t.task) {
		return nil
	}
	if t.p.json {
		return t.p.value(l)
	}
	text := l.Text
	if t.prefix {
		text = l.AgentID + ": " + text
	}
	return t.p.text(text)
}

func (t *tailer) fetch(ctx context.Context, id string) error {
	for {
		since, resuming := t.last[id]
		limit := t.lines
		query := url.Values{}
		if resuming {
			limit = tailMaxLines
			query.Set("since", strconv.FormatInt(since, 10))
		}
		query.Set("limit", strconv.Itoa(limit))
		if t.task != "" {
			query.Set("task_id", t.task)
		}
		resp, err := t.c.do(ctx, request{method: http.MethodGet, path: "agents/" + id + "/output", query: query})
		if err != nil {
			return err
		}
		var out struct {
			Lines []outputLine `json:"lines"`
			More  bool         `json:"more"`
			Gap   bool         `json:"gap"`
		}
		if err := json.Unmarshal(resp.body, &out); err != nil {
			return fmt.Errorf("invalid agent output response: %w", err)
		}
		if out.Gap && resuming {
			delete(t.last, id)
			continue
		}
		for _, l := range out.Lines {
			if l.AgentID == "" {
				l.AgentID = id
			}
			if err := t.emit(l); err != nil {
				return err
			}
		}
		if !resuming || !out.More || len(out.Lines) == 0 {
			return nil
		}
	}
}

func (t *tailer) follow(ctx context.Context, ids []string) error {
	topics := "output"
	if len(ids) > 0 {
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = "output:" + id
		}
		topics = strings.Join(parts, ",")
	}
	connected := false
	for {
		err := t.c.stream(ctx, request{method: http.MethodGet, path: "events", query: url.Values{"topics": {topics}}}, func(ev sseEvent) error {
			connected = true
			switch ev.name {
			case "hello":
				for _, id := range ids {
					if err := t.fetch(ctx, id); err != nil {
						return err
					}
				}
			case "agent.output":
				var msg struct {
					Data struct {
						AgentID string       `json:"agent_id"`
						Lines   []outputLine `json:"lines"`
					} `json:"data"`
				}
				if err := json.Unmarshal([]byte(ev.data), &msg); err != nil {
					return fmt.Errorf("invalid agent.output event: %w", err)
				}
				for _, l := range msg.Data.Lines {
					if l.AgentID == "" {
						l.AgentID = msg.Data.AgentID
					}
					if err := t.emit(l); err != nil {
						return err
					}
				}
			}
			return nil
		})
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			var ce *cliError
			if !errors.As(err, &ce) || ce.exit != exitUnavailable || !connected {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(tailReconnect):
		}
	}
}

func parseOutputTail(args []string, stdin io.Reader) (command, error) {
	set := flags("output tail")
	task := set.String("task", "", "only lines tagged with this task")
	lines := set.Int("lines", tailBacklog, "backlog lines per agent")
	follow := set.Bool("follow", false, "keep streaming new lines")
	set.BoolVar(follow, "f", false, "same as --follow")
	all := set.Bool("all", false, "follow every agent (requires --follow)")
	if err := parseFlags(set, args, 0, maxBulk); err != nil {
		return nil, err
	}
	if *lines < 1 || *lines > tailMaxLines {
		return nil, fmt.Errorf("--lines must be in 1..%d", tailMaxLines)
	}
	var ids []string
	if set.NArg() > 0 {
		var err error
		if ids, err = collectIDs(set.Args(), stdin); err != nil {
			return nil, err
		}
	}
	switch {
	case *all && len(ids) > 0:
		return nil, errors.New("output tail takes agent IDs or --all, not both")
	case !*all && len(ids) == 0:
		return nil, errors.New("output tail requires AGENT_ID... or --all")
	case *all && !*follow:
		return nil, errors.New("--all requires --follow (see output latest for a snapshot)")
	}
	if *task != "" {
		if _, err := identifier(*task); err != nil {
			return nil, err
		}
	}
	return func(ctx context.Context, c *conn, p *printer) error {
		t := &tailer{c: c, p: p, task: *task, lines: *lines, prefix: *all || len(ids) > 1, last: map[string]int64{}}
		if *follow {
			return t.follow(ctx, ids)
		}
		for _, id := range ids {
			if err := t.fetch(ctx, id); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

var outputCommands = table{
	"tail":   parseOutputTail,
	"latest": plain("output latest", request{method: http.MethodGet, path: "agent-output"}),
}

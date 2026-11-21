package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

func parseTranscript(args []string, _ io.Reader) (command, error) {
	set := flags("tasks transcript")
	since := set.Int64("since", 0, "return lines after this offset")
	limit := set.Int("limit", 0, "maximum lines (default 500, at most 2000)")
	at := set.String("at", "", "start at the first line at or after this RFC 3339 time")
	query := set.String("q", "", "only lines containing this text")
	if err := parseFlags(set, args, 1, 1); err != nil {
		return nil, err
	}
	id, err := identifier(set.Arg(0))
	if err != nil {
		return nil, err
	}
	if *since < 0 || *limit < 0 {
		return nil, fmt.Errorf("--since and --limit must not be negative")
	}
	q := setIf(url.Values{}, "at", *at, "q", *query)
	if *since > 0 {
		q.Set("since", strconv.FormatInt(*since, 10))
	}
	if *limit > 0 {
		q.Set("limit", strconv.Itoa(*limit))
	}
	return get("tasks/"+id+"/transcript", q), nil
}

func parseTranscriptDownload(args []string, _ io.Reader) (command, error) {
	set := flags("tasks transcript-download")
	out := set.String("out", "", "destination file or - for stdout")
	if err := parseFlags(set, args, 1, 1); err != nil {
		return nil, err
	}
	id, err := identifier(set.Arg(0))
	if err != nil {
		return nil, err
	}
	if *out == "" {
		return nil, fmt.Errorf("tasks transcript-download requires --out")
	}
	req := request{
		method: http.MethodGet, path: "tasks/" + id + "/transcript/download", accept: "*/*",
		maxResponse: transferMax, timeout: transferTimeout,
	}
	return func(ctx context.Context, c *conn, p *printer) error {
		resp, err := c.do(ctx, req)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(resp.body)
		return save(*out, resp.body, p, map[string]any{"task_id": id, "sha256": hex.EncodeToString(sum[:])})
	}, nil
}

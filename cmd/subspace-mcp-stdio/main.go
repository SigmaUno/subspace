// Command subspace-mcp-stdio bridges stdio MCP to the Subspace HTTP MCP endpoint.
//
// It reads newline-delimited JSON-RPC messages from stdin, POSTs each one to
// $SUBSPACE_URL/mcp with "Authorization: Bearer $SUBSPACE_TOKEN" and writes the
// responses to stdout, one JSON message per line. Logs go to stderr only and
// never contain the token.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

const maxErrBody = 512

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Stdin, os.Stdout, os.Stderr, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "subspace-mcp-stdio:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, in io.Reader, out, errw io.Writer, getenv func(string) string) error {
	base := strings.TrimRight(strings.TrimSpace(getenv("SUBSPACE_URL")), "/")
	token := strings.TrimSpace(getenv("SUBSPACE_TOKEN"))
	if base == "" || token == "" {
		return errors.New("SUBSPACE_URL and SUBSPACE_TOKEN must be set")
	}
	b := &bridge{
		url:    base + "/mcp",
		token:  token,
		client: &http.Client{},
		out:    out,
		log:    slog.New(slog.NewTextHandler(errw, nil)),
	}
	return b.serve(ctx, in)
}

type bridge struct {
	url       string
	token     string
	sessionID string
	client    *http.Client
	out       io.Writer
	log       *slog.Logger
}

type rpcHead struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
}

func (b *bridge) serve(ctx context.Context, in io.Reader) error {
	r := bufio.NewReader(in)
	for {
		line, err := r.ReadBytes('\n')
		if msg := bytes.TrimSpace(line); len(msg) > 0 {
			if herr := b.handle(ctx, msg); herr != nil {
				return herr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read stdin: %w", err)
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

// handle forwards one message. Only stdout write failures are fatal.
func (b *bridge) handle(ctx context.Context, msg []byte) error {
	var head rpcHead
	_ = json.Unmarshal(msg, &head)
	hasID := len(head.ID) > 0 && string(head.ID) != "null"

	if err := b.forward(ctx, msg, head); err != nil {
		var we *writeError
		if errors.As(err, &we) {
			return we.err
		}
		b.log.Error("request failed", "method", head.Method, "err", err.Error())
		if hasID {
			return b.writeLine(rpcError(head.ID, -32000, "subspace bridge: "+err.Error()))
		}
	}
	return nil
}

type writeError struct{ err error }

func (e *writeError) Error() string { return e.err.Error() }

func (b *bridge) forward(ctx context.Context, msg []byte, head rpcHead) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.url, bytes.NewReader(msg))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if b.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", b.sessionID)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return sanitize(err, b.token)
	}
	defer func() { _ = resp.Body.Close() }()

	if head.Method == "initialize" && resp.StatusCode < 300 {
		if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
			b.sessionID = sid
		}
	}

	if resp.StatusCode == http.StatusAccepted {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if resp.StatusCode == http.StatusNotFound && b.sessionID != "" && head.Method != "initialize" {
		b.log.Warn("session expired; re-initialize required")
		b.sessionID = ""
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		return fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mt == "text/event-stream" {
		return b.copySSE(resp.Body)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil
	}
	return b.writeLine(body)
}

// copySSE writes the data payload of every SSE event as one line.
func (b *bridge) copySSE(body io.Reader) error {
	r := bufio.NewReader(body)
	var data [][]byte
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		payload := bytes.Join(data, []byte("\n"))
		data = nil
		return b.writeLine(payload)
	}
	for {
		line, err := r.ReadBytes('\n')
		line = bytes.TrimRight(line, "\r\n")
		switch {
		case len(line) == 0 && err == nil:
			if ferr := flush(); ferr != nil {
				return ferr
			}
		case bytes.HasPrefix(line, []byte("data:")):
			d := bytes.TrimPrefix(line, []byte("data:"))
			d = bytes.TrimPrefix(d, []byte(" "))
			data = append(data, append([]byte(nil), d...))
		}
		if err != nil {
			if ferr := flush(); ferr != nil {
				return ferr
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// writeLine writes one JSON message as a single line. Payloads that are valid
// JSON are compacted so embedded newlines cannot break the framing.
func (b *bridge) writeLine(payload []byte) error {
	var buf bytes.Buffer
	if err := json.Compact(&buf, payload); err != nil {
		buf.Reset()
		buf.Write(bytes.ReplaceAll(payload, []byte("\n"), []byte(" ")))
	}
	buf.WriteByte('\n')
	if _, err := b.out.Write(buf.Bytes()); err != nil {
		return &writeError{fmt.Errorf("write stdout: %w", err)}
	}
	return nil
}

func rpcError(id json.RawMessage, code int, message string) []byte {
	out, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
	return out
}

// sanitize makes sure the token can never end up in an error message.
func sanitize(err error, token string) error {
	if token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "[redacted]"))
}

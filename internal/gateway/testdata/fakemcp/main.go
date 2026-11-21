package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

type request struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"params"`
}

func main() {
	out := bufio.NewWriter(os.Stdout)
	send := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = out.Write(append(b, '\n'))
		_ = out.Flush()
	}
	text := func(id json.RawMessage, s string) {
		send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": s}}}})
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	for in.Scan() {
		var req request
		if json.Unmarshal(in.Bytes(), &req) != nil || len(req.ID) == 0 {
			continue
		}
		switch req.Method {
		case "initialize":
			send(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{"name": "fake", "version": "1"}}})
		case "tools/list":
			var tools []any
			for _, n := range []string{"echo", "env", "crash", "flood", "spawn", "deaf"} {
				tools = append(tools, map[string]any{"name": n, "description": "fake " + n,
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}})
			}
			send(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": tools}})
		case "tools/call":
			switch req.Params.Name {
			case "echo":
				s, _ := req.Params.Arguments["text"].(string)
				text(req.ID, s)
			case "env":
				env := os.Environ()
				sort.Strings(env)
				text(req.ID, strings.Join(env, "\n"))
			case "deaf":
				time.Sleep(time.Hour)
			case "crash":
				os.Exit(3)
			case "flood":
				_, _ = out.WriteString(strings.Repeat("x", 3<<20))
				_ = out.Flush()
			case "spawn":
				cmd := exec.Command("sleep", "300")
				if err := cmd.Start(); err != nil {
					text(req.ID, "error "+err.Error())
					continue
				}
				text(req.ID, strconv.Itoa(cmd.Process.Pid))
			}
		default:
			send(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "unknown"}})
		}
	}
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/SigmaUno/subspace/internal/client"
)

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "subspace-e2e:", err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, out io.Writer) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("usage: subspace-e2e <tool> [json-arguments]")
	}
	url, token := getenv("SUBSPACE_URL"), getenv("SUBSPACE_TOKEN")
	if url == "" || token == "" {
		return fmt.Errorf("SUBSPACE_URL and SUBSPACE_TOKEN are required")
	}
	var arguments map[string]any
	if len(args) == 2 {
		if err := json.Unmarshal([]byte(args[1]), &arguments); err != nil {
			return fmt.Errorf("arguments: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	mcp := client.NewMCP(url+"/mcp", token, client.WithClientName("subspace-e2e"))
	if err := mcp.Initialize(ctx); err != nil {
		return err
	}
	defer func() { _ = mcp.Close(context.Background()) }()
	var result json.RawMessage
	if err := mcp.Call(ctx, args[0], arguments, &result); err != nil {
		return err
	}
	_, err := fmt.Fprintln(out, string(result))
	return err
}

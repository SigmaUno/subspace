package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/spawner"
)

func main() {
	if err := run(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "gen-api-docs:", err)
		os.Exit(1)
	}
}

func run(w io.Writer) error {
	dir, err := os.MkdirTemp("", "gen-api-docs-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := server.New(ctx, server.Config{
		DBPath:     filepath.Join(dir, "gen.db"),
		OwnerToken: hex.EncodeToString(raw),
		Spawner:    spawner.Noop{},
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	_, err = io.WriteString(w, mcp.RenderToolMatrix(s.ToolNames()))
	return err
}

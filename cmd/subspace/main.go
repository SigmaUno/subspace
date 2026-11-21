package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/version"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func dur(k, def string) time.Duration {
	d, err := time.ParseDuration(env(k, def))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid duration in %s\n", k)
		os.Exit(2)
	}
	return d
}

func ownerToken() (string, error) {
	tok := os.Getenv("SUBSPACE_OWNER_TOKEN")
	if f := os.Getenv("SUBSPACE_OWNER_TOKEN_FILE"); f != "" {
		b, err := os.ReadFile(f) //nolint:gosec // operator-supplied path
		if err != nil {
			return "", err
		}
		tok = strings.TrimSpace(string(b))
	}
	return tok, os.Unsetenv("SUBSPACE_OWNER_TOKEN")
}

func count(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		fmt.Fprintf(os.Stderr, "invalid count in %s\n", k)
		os.Exit(2)
	}
	return n
}

func main() {
	if len(os.Args) > 1 {
		if run, ok := map[string]func([]string, io.Writer) error{"backup": runBackup, "restore": runRestore, "audit-verify": runAuditVerify, "export": runExport, "import": runImport, "retention-restore": runRetentionRestore}[os.Args[1]]; ok {
			if err := run(os.Args[2:], os.Stdout); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			return
		}
	}
	level := slog.LevelInfo
	if strings.EqualFold(os.Getenv("SUBSPACE_LOG_LEVEL"), "debug") {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	tok, err := ownerToken()
	if err != nil {
		log.Error("read owner token file", "err", err)
		os.Exit(2)
	}
	board.Version = env("SUBSPACE_VERSION", version.Version)
	cfg := server.Config{
		Addr: env("SUBSPACE_ADDR", ":8080"), DBPath: env("SUBSPACE_DB", "/data/subspace.db"), OwnerToken: tok,
		PublicURL: env("SUBSPACE_PUBLIC_URL", "http://localhost:8080"), CapacityFile: os.Getenv("SUBSPACE_CAPACITY_FILE"), PromptsDir: os.Getenv("SUBSPACE_PROMPTS_DIR"),
		Lease: dur("SUBSPACE_LEASE", "15m"), PoisonExpiries: count("SUBSPACE_POISON_EXPIRIES", board.DefaultPoisonExpiries), BackupInterval: dur("SUBSPACE_BACKUP_INTERVAL", "24h"),
		BackupDir: os.Getenv("SUBSPACE_BACKUP_DIR"), BackupKeep: count("SUBSPACE_BACKUP_KEEP", 14),
		ShutdownTimeout: dur("SUBSPACE_SHUTDOWN_TIMEOUT", "20s"), Log: log,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s, err := server.New(ctx, cfg)
	if err != nil {
		log.Error("start", "err", err)
		os.Exit(1)
	}
	defer func() { _ = s.Close() }()
	log.Info("subspace listening", "addr", cfg.Addr, "db", cfg.DBPath)
	if err := s.Run(ctx); err != nil {
		log.Error("server", "err", err)
		os.Exit(1)
	}
}

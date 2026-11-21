package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "subspace-egress:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("subspace-egress", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", ":3128", "listen address")
	allow := fs.String("allow", "", "comma-separated host names (exact, or *.domain for subdomains) agents may reach")
	private := fs.String("private", "", "comma-separated allowed hosts that may resolve to private addresses")
	ports := fs.String("ports", "443,80", "comma-separated destination ports")
	pinTTL := fs.Duration("pin-ttl", 5*time.Minute, "how long a resolved address set stays pinned")
	idle := fs.Duration("idle", 5*time.Minute, "idle timeout of a tunnel")
	maxConns := fs.Int("max-conns", 256, "maximum concurrent connections")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	var portList []int
	for _, s := range split(*ports) {
		n, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("invalid port %q", s)
		}
		portList = append(portList, n)
	}
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	p, err := newProxy(options{
		allow: split(*allow), private: split(*private), ports: portList,
		pinTTL: *pinTTL, idle: *idle, maxConns: *maxConns, log: log,
	})
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: *listen, Handler: p, ReadHeaderTimeout: 10 * time.Second, ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn)}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	log.Info("egress proxy listening", "addr", *listen, "allow", p.allow)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

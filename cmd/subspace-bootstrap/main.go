package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/doctor"
)

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

var doctorEnv = doctor.SystemEnv

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

type common struct {
	url       string
	tokenFile string
	timeout   time.Duration
}

func (c *common) register(fs *flag.FlagSet, getenv func(string) string) {
	fs.StringVar(&c.url, "url", getenv("SUBSPACE_URL"), "Subspace base URL (env SUBSPACE_URL)")
	fs.StringVar(&c.tokenFile, "token-file", getenv("SUBSPACE_OWNER_TOKEN_FILE"), "file with the owner token (env SUBSPACE_OWNER_TOKEN_FILE, else SUBSPACE_OWNER_TOKEN)")
	fs.DurationVar(&c.timeout, "timeout", 2*time.Minute, "overall time limit")
}

func (c *common) token(getenv func(string) string) (string, error) {
	if c.tokenFile != "" {
		b, err := os.ReadFile(c.tokenFile)
		if err != nil {
			return "", fmt.Errorf("owner token file: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	if t := strings.TrimSpace(getenv("SUBSPACE_OWNER_TOKEN")); t != "" {
		return t, nil
	}
	return "", errors.New("owner token missing: set SUBSPACE_OWNER_TOKEN, SUBSPACE_OWNER_TOKEN_FILE or -token-file")
}

func (c *common) check() error {
	if c.url == "" {
		return errors.New("server URL missing: set SUBSPACE_URL or -url")
	}
	return nil
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "preflight" {
		return runPreflight(ctx, args[1:], getenv, stdout, stderr)
	}
	fs := flag.NewFlagSet("subspace-bootstrap", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: subspace-bootstrap -config bootstrap.json [-dry-run] [-approve-plan] [-show]\n       subspace-bootstrap preflight [-json] [-skip-notify]")
		fs.PrintDefaults()
	}
	var c common
	c.register(fs, getenv)
	cfgPath := fs.String("config", "", "bootstrap config (JSON)")
	dry := fs.Bool("dry-run", false, "print what would change and change nothing")
	approve := fs.Bool("approve-plan", false, "approve the brief's proposed plan after the lint and estimate preview")
	show := fs.Bool("show", false, "also print join lines, tokens and MCP snippets of new agents to stdout")
	if err := fs.Parse(args); err != nil {
		return usage(err)
	}
	if fs.NArg() > 0 || *cfgPath == "" {
		fs.Usage()
		return exitUsage
	}
	cfg, err := loadConfig(*cfgPath)
	if err == nil {
		err = c.check()
	}
	tok := ""
	if err == nil {
		tok, err = c.token(getenv)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "subspace-bootstrap:", err)
		return exitUsage
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	b := &bootstrap{
		o:   client.NewOwner(c.url, tok, client.WithClientName("subspace-bootstrap"), client.WithRetries(2)),
		cfg: cfg, dry: *dry, approve: *approve || cfg.ApprovePlan, show: *show, out: stdout,
	}
	if err := b.run(ctx); err != nil {
		_, _ = fmt.Fprintln(stderr, "subspace-bootstrap:", err)
		return exitFailure
	}
	return exitOK
}

func runPreflight(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("subspace-bootstrap preflight", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	c.register(fs, getenv)
	asJSON := fs.Bool("json", false, "print the result as JSON")
	skipNotify := fs.Bool("skip-notify", false, "do not send a test notification")
	if err := fs.Parse(args); err != nil {
		return usage(err)
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return exitUsage
	}
	err := c.check()
	tok := ""
	if err == nil {
		tok, err = c.token(getenv)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "subspace-bootstrap preflight:", err)
		return exitUsage
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	denv := doctorEnv()
	denv.Getenv = getenv
	p := &preflight{
		o:    client.NewOwner(c.url, tok, client.WithClientName("subspace-bootstrap")),
		base: c.url, token: tok, skipNotify: *skipNotify, doctor: denv, now: time.Now,
		hc: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	v := p.run(ctx)
	if err := v.write(stdout, *asJSON); err != nil {
		_, _ = fmt.Fprintln(stderr, "subspace-bootstrap preflight:", err)
		return exitFailure
	}
	if v.Verdict != statusGo {
		return exitFailure
	}
	return exitOK
}

func usage(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	return exitUsage
}

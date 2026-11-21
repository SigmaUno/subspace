package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
)

var scenarios = []Scenario{
	{Name: "server-restart", Desc: "kill and restart the server under load", Mode: Local, Run: serverRestart},
	{Name: "db-busy-storm", Desc: "hold the SQLite write lock in bursts while agents write", Mode: Local, Run: dbBusyStorm},
	{Name: "network-partition", Desc: "cut agents off from the server, then heal", Mode: Local, Run: networkPartition},
	{Name: "network-latency", Desc: "add latency between agents and server", Mode: Local, Run: networkLatency},
	{Name: "runaway-agent", Desc: "an agent repeats one call until loop protection pauses it", Run: runawayAgent},
	{Name: "leaked-token", Desc: "an agent posts another agent's token", Run: leakedToken},
	{Name: "budget-exhaustion", Desc: "spend passes the organisation cap", Mode: Disruptive, Run: budgetExhaustion},
	{Name: "webhook-down", Desc: "outbound webhook receiver fails, retries, dead-letters, recovers", Mode: Local, Run: webhookDown},
	{Name: "sse-storm", Desc: "hundreds of event streams, some stalled, during an event burst", Run: sseStorm},
	{Name: "backup-corruption", Desc: "newest backup is corrupted, restore drills alert", Mode: Local, Run: backupCorruption},
	{Name: "clock-jump", Desc: "the server clock jumps forward and back", Mode: Local, Run: clockJump},
	{Name: "leader-loss", Desc: "the HA leader dies, another instance takes over", Mode: Local, PG: true, Run: leaderLoss},
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("subspace-gameday", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var opt Options
	url := fs.String("url", os.Getenv("SUBSPACE_URL"), "target Subspace URL; empty runs an in-process stack (env SUBSPACE_URL)")
	tokenFile := fs.String("owner-token-file", os.Getenv("SUBSPACE_OWNER_TOKEN_FILE"), "file with the owner token (env SUBSPACE_OWNER_TOKEN_FILE, else SUBSPACE_OWNER_TOKEN)")
	only := fs.String("scenario", "all", "comma-separated scenario names, or all")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	listOnly := fs.Bool("list", false, "list scenarios and exit")
	fs.BoolVar(&opt.Disruptive, "disruptive", false, "allow scenarios that pause or reconfigure a remote target")
	fs.DurationVar(&opt.Detect, "detect", 10*time.Second, "an alert or decision must be raised within this long")
	fs.DurationVar(&opt.Recover, "recover", 20*time.Second, "recovery must complete within this long")
	fs.DurationVar(&opt.Lease, "lease", 3*time.Second, "lease of in-process stacks")
	fs.IntVar(&opt.Agents, "agents", 4, "worker agents in load scenarios")
	fs.IntVar(&opt.Subs, "subscribers", 200, "event streams in sse-storm")
	fs.IntVar(&opt.Burst, "burst", 600, "events published in sse-storm")
	fs.DurationVar(&opt.Storm, "storm", time.Second, "length of each database lock hold")
	fs.Uint64Var(&opt.Seed, "seed", 1, "seed of injected network faults")
	fs.StringVar(&opt.PGDSN, "pg-dsn", os.Getenv("SUBSPACE_TEST_PG_DSN"), "Postgres DSN for leader-loss (env SUBSPACE_TEST_PG_DSN)")
	level := fs.String("log-level", "error", "log level (debug, info, warn, error)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *listOnly {
		for _, s := range scenarios {
			_, _ = fmt.Fprintf(stdout, "%-18s %s\n", s.Name, s.Desc)
		}
		return 0
	}
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(*level)); err != nil {
		_, _ = fmt.Fprintln(stderr, "log-level:", err)
		return 2
	}
	opt.Log = slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: lv}))
	opt.URL = *url
	if opt.remote() {
		tok, err := ownerToken(*tokenFile)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 2
		}
		opt.Token = tok
	}
	picked, err := pick(*only)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	results := runAll(ctx, opt, picked)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"passed": passed(results), "scenarios": results})
	} else {
		table(stdout, results)
	}
	if !passed(results) || ctx.Err() != nil {
		return 1
	}
	return 0
}

func ownerToken(file string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file) //nolint:gosec // operator supplied path
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	if t := os.Getenv("SUBSPACE_OWNER_TOKEN"); t != "" {
		return t, nil
	}
	return "", fmt.Errorf("owner token needed: -owner-token-file, SUBSPACE_OWNER_TOKEN_FILE or SUBSPACE_OWNER_TOKEN")
}

func pick(spec string) ([]Scenario, error) {
	if spec == "" || spec == "all" {
		return scenarios, nil
	}
	var out []Scenario
	for name := range strings.SplitSeq(spec, ",") {
		name = strings.TrimSpace(name)
		found := false
		for _, s := range scenarios {
			if s.Name == name {
				out = append(out, s)
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown scenario %q", name)
		}
	}
	return out, nil
}

func ms(v *int64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%dms", *v)
}

func table(w io.Writer, rs []Result) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SCENARIO\tRESULT\tDETECT\tRECOVER\tTOTAL\tDETAIL")
	for _, r := range rs {
		detail := r.Note
		failed := 0
		for _, c := range r.Checks {
			if !c.OK {
				failed++
				if detail == "" {
					detail = c.Name + ": " + c.Detail
				}
			}
		}
		if r.Error != "" {
			detail = r.Error
		}
		if r.Status == Pass {
			detail = fmt.Sprintf("%d checks", len(r.Checks))
		} else if failed > 1 {
			detail += fmt.Sprintf(" (+%d more)", failed-1)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%dms\t%s\n", r.Name, strings.ToUpper(string(r.Status)), ms(r.DetectMS), ms(r.RecoverMS), r.DurationMS, detail)
	}
	_ = tw.Flush()
	pass, fail, skip := 0, 0, 0
	for _, r := range rs {
		switch r.Status {
		case Pass:
			pass++
		case Fail:
			fail++
		case Skip:
			skip++
		}
	}
	_, _ = fmt.Fprintf(w, "\n%d passed, %d failed, %d skipped\n", pass, fail, skip)
}

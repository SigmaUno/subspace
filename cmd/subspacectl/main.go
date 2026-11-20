package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const usage = `Usage: subspacectl COMMAND [OPTIONS]

Set SUBSPACE_OWNER_TOKEN and optionally SUBSPACE_URL (default http://localhost:8080).
Successful commands write the server's JSON response to stdout.

Commands:
  create-brief --title TITLE (--body TEXT | --body-file FILE)
               [--repos a/b,c/d] [--allowed-providers openai,deepseek]
               [--max-cost-eur 50] [--max-parallel-agents 10]
  briefs [--status open|closed] [--limit 1000]
  board BRIEF_ID
  approve-plan PLAN_ID [--note TEXT]
  reject-plan PLAN_ID --reason TEXT
  answer QUESTION_ID --answer TEXT
  pause-all [--reason TEXT]
  resume-all
  register-agent AGENT_ID --role ROLE --kind KIND --model MODEL [--provider NAME]
  register-agents --prefix PREFIX --count N --role ROLE --kind KIND --model MODEL
                  [--provider NAME]
  revoke-agent AGENT_ID [--reason TEXT]
  agents [--role ROLE] [--status STATUS] [--limit 1000]
  capacity get
  capacity set --file FILE
  join-snippet AGENT_ID

Use "-" for stdin with --body-file or --file. Registration results contain
one-time credentials. join-snippet returns configuration with token placeholders.
`

type request struct {
	method string
	path   string
	query  url.Values
	body   any
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "subspacectl:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(stdout, usage)
		return err
	}
	req, err := parse(args, stdin)
	if errors.Is(err, flag.ErrHelp) {
		_, err := io.WriteString(stdout, usage)
		return err
	}
	if err != nil {
		return err
	}
	token := strings.TrimSpace(getenv("SUBSPACE_OWNER_TOKEN"))
	if token == "" {
		return errors.New("set SUBSPACE_OWNER_TOKEN before making API calls")
	}
	base := strings.TrimSpace(getenv("SUBSPACE_URL"))
	if base == "" {
		base = "http://localhost:8080"
	}
	return send(ctx, base, token, req, stdout)
}

func send(ctx context.Context, base, token string, req request, out io.Writer) error {
	const maxResponse = 32 << 20
	endpoint, err := url.Parse(base)
	if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") ||
		endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Opaque != "" {
		return errors.New("SUBSPACE_URL must be an HTTP(S) base URL without credentials, query or fragment")
	}
	endpoint = endpoint.JoinPath("api/v1", req.path)
	endpoint.RawQuery = req.query.Encode()
	var data []byte
	if req.body != nil {
		data, err = json.Marshal(req.body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.method, endpoint.String(), bytes.NewReader(data))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "application/json")
	if req.body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	client := http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("contact Subspace: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return fmt.Errorf("read Subspace response: %w", err)
	}
	if len(payload) > maxResponse {
		return errors.New("response exceeds 32 MiB")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &failure) == nil && failure.Error.Message != "" {
			if failure.Error.Code != "" {
				return fmt.Errorf("HTTP %d (%s): %s", resp.StatusCode, failure.Error.Code, failure.Error.Message)
			}
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, failure.Error.Message)
		}
		return fmt.Errorf("unexpected Subspace response: HTTP %d", resp.StatusCode)
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, bytes.TrimSpace(payload), "", "  "); err != nil {
		return fmt.Errorf("invalid JSON in Subspace response: %w", err)
	}
	formatted.WriteByte('\n')
	_, err = formatted.WriteTo(out)
	return err
}

func parse(args []string, stdin io.Reader) (request, error) {
	command, args := args[0], args[1:]
	switch command {
	case "create-brief":
		return parseBrief(args, stdin)
	case "briefs", "agents":
		return parseList(command, args)
	case "board", "join-snippet":
		id, err := parseID(command, args)
		if command == "board" {
			return request{method: http.MethodGet, path: "briefs/" + id + "/board"}, err
		}
		return request{method: http.MethodGet, path: "agents/" + id + "/connect"}, err
	case "approve-plan", "reject-plan", "answer", "pause-all", "revoke-agent", "resume-all":
		return parseAction(command, args)
	case "register-agent", "register-agents":
		return parseRegistration(command, args)
	case "capacity":
		return parseCapacity(args, stdin)
	default:
		return request{}, fmt.Errorf("unknown command %q (see --help)", command)
	}
}

func flags(command string) *flag.FlagSet {
	set := flag.NewFlagSet(command, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	return set
}

func parseFlags(set *flag.FlagSet, args []string, minArgs, maxArgs int) error {
	var options, positions []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positions = append(positions, args[i+1:]...)
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			positions = append(positions, arg)
			continue
		}
		options = append(options, arg)
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if !hasValue && set.Lookup(name) != nil {
			if i+1 == len(args) || args[i+1] == "--" {
				return fmt.Errorf("%s: %s requires a value", set.Name(), arg)
			}
			i++
			options = append(options, args[i])
		}
	}
	if err := set.Parse(append(append(options, "--"), positions...)); err != nil {
		return fmt.Errorf("%s: %w", set.Name(), err)
	}
	if set.NArg() < minArgs || set.NArg() > maxArgs {
		if maxArgs == 0 {
			return fmt.Errorf("%s: unexpected positional arguments (see --help)", set.Name())
		}
		return fmt.Errorf("%s: expected %d positional argument(s) (see --help)", set.Name(), minArgs)
	}
	return nil
}

func parseID(command string, args []string) (string, error) {
	set := flags(command)
	if err := parseFlags(set, args, 1, 1); err != nil {
		return "", err
	}
	return identifier(set.Arg(0))
}

var validIdentifier = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func identifier(id string) (string, error) {
	if !validIdentifier.MatchString(id) {
		return "", fmt.Errorf("invalid identifier %q", id)
	}
	return id, nil
}

func parseList(command string, args []string) (request, error) {
	set := flags(command)
	status := set.String("status", "", "filter by status")
	limit := set.Int("limit", 1000, "maximum results")
	var role *string
	if command == "agents" {
		role = set.String("role", "", "filter by role")
	}
	if err := parseFlags(set, args, 0, 0); err != nil {
		return request{}, err
	}
	if *limit < 1 || *limit > 1000 {
		return request{}, errors.New("--limit must be in 1..1000")
	}
	query := url.Values{"limit": {strconv.Itoa(*limit)}}
	if *status != "" {
		query.Set("status", *status)
	}
	if role != nil && *role != "" {
		query.Set("role", *role)
	}
	return request{method: http.MethodGet, path: command, query: query}, nil
}

func parseBrief(args []string, stdin io.Reader) (request, error) {
	set := flags("create-brief")
	title := set.String("title", "", "brief title")
	body := set.String("body", "", "brief text")
	file := set.String("body-file", "", "read brief from file or stdin")
	repos := set.String("repos", "", "comma-separated repositories")
	providers := set.String("allowed-providers", "", "comma-separated providers")
	cost := set.Float64("max-cost-eur", 50, "budget in EUR")
	parallel := set.Int("max-parallel-agents", 10, "parallel agent limit")
	if err := parseFlags(set, args, 0, 0); err != nil {
		return request{}, err
	}
	if strings.TrimSpace(*title) == "" || (*body == "") == (*file == "") {
		return request{}, errors.New("create-brief requires --title and one of --body or --body-file")
	}
	if *file != "" {
		data, err := readInput(*file, stdin)
		if err != nil {
			return request{}, fmt.Errorf("read brief: %w", err)
		}
		*body = string(data)
	}
	if strings.TrimSpace(*body) == "" || *cost < 0 || *parallel < 1 {
		return request{}, errors.New("brief body must be nonempty, cost nonnegative and parallel limit positive")
	}
	return request{method: http.MethodPost, path: "briefs", body: map[string]any{
		"title": *title, "body": *body, "repos": splitList(*repos), "allowed_providers": splitList(*providers),
		"max_cost_eur": *cost, "max_parallel_agents": *parallel,
	}}, nil
}

func splitList(value string) []string {
	list := []string{}
	for _, entry := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			list = append(list, trimmed)
		}
	}
	return list
}

func readInput(path string, stdin io.Reader) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(stdin)
	}
	return os.ReadFile(path)
}

func parseAction(command string, args []string) (request, error) {
	type action struct {
		path, field string
		needsID     bool
		required    bool
	}
	actions := map[string]action{
		"approve-plan": {"plans/%s/approve", "note", true, false},
		"reject-plan":  {"plans/%s/reject", "reason", true, true},
		"answer":       {"questions/%s/answer", "answer", true, true},
		"pause-all":    {"pause-all", "reason", false, false},
		"resume-all":   {"resume-all", "", false, false},
		"revoke-agent": {"agents/%s/revoke", "reason", true, false},
	}
	actionConfig := actions[command]
	set := flags(command)
	var text string
	if actionConfig.field != "" {
		set.StringVar(&text, actionConfig.field, "", "text sent to the server")
	}
	count := 0
	if actionConfig.needsID {
		count = 1
	}
	if err := parseFlags(set, args, count, count); err != nil {
		return request{}, err
	}
	if actionConfig.required && strings.TrimSpace(text) == "" {
		return request{}, fmt.Errorf("%s requires --%s", command, actionConfig.field)
	}
	path := actionConfig.path
	if actionConfig.needsID {
		id, err := identifier(set.Arg(0))
		if err != nil {
			return request{}, err
		}
		path = fmt.Sprintf(path, id)
	}
	body := map[string]string{}
	if actionConfig.field != "" {
		body[actionConfig.field] = text
	}
	return request{method: http.MethodPost, path: path, body: body}, nil
}

func parseRegistration(command string, args []string) (request, error) {
	set := flags(command)
	role := set.String("role", "", "agent role")
	kind := set.String("kind", "", "runtime kind")
	model := set.String("model", "", "model name")
	provider := set.String("provider", "", "override the provider for the kind")
	bulk := command == "register-agents"
	var id, prefix string
	var count int
	if bulk {
		set.StringVar(&prefix, "prefix", "", "agent ID prefix")
		set.IntVar(&count, "count", 0, "number of agents to register")
	} else {
		set.StringVar(&id, "id", "", "agent ID (alternative to positional argument)")
	}
	maxArgs := 1
	if bulk {
		maxArgs = 0
	}
	if err := parseFlags(set, args, 0, maxArgs); err != nil {
		return request{}, err
	}
	if strings.TrimSpace(*role) == "" || strings.TrimSpace(*kind) == "" || strings.TrimSpace(*model) == "" {
		return request{}, fmt.Errorf("%s requires --role, --kind and --model", command)
	}
	body := map[string]any{"role": *role, "kind": *kind, "model": *model, "provider": *provider}
	path := "agents"
	if bulk {
		if _, err := identifier(prefix); err != nil || count < 1 || count > 1000 {
			return request{}, errors.New("register-agents requires a valid --prefix and --count in 1..1000")
		}
		path = "agents/bulk"
		body["prefix"], body["count"] = prefix, count
	} else {
		if id != "" && set.NArg() != 0 {
			return request{}, errors.New("specify the agent ID once")
		}
		if id == "" {
			id = set.Arg(0)
		}
		if _, err := identifier(id); err != nil {
			return request{}, err
		}
		body["id"] = id
	}
	return request{method: http.MethodPost, path: path, body: body}, nil
}

func parseCapacity(args []string, stdin io.Reader) (request, error) {
	if len(args) == 0 {
		return request{}, errors.New("capacity requires get or set")
	}
	switch args[0] {
	case "--help", "-h":
		return request{}, flag.ErrHelp
	case "get":
		err := parseFlags(flags("capacity get"), args[1:], 0, 0)
		return request{method: http.MethodGet, path: "capacity"}, err
	case "set":
		set := flags("capacity set")
		file := set.String("file", "", "JSON config file or - for stdin")
		if err := parseFlags(set, args[1:], 0, 0); err != nil {
			return request{}, err
		}
		if *file == "" {
			return request{}, errors.New("capacity set requires --file")
		}
		body, err := readInput(*file, stdin)
		if err != nil {
			return request{}, fmt.Errorf("read capacity: %w", err)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(body, &object); err != nil || object == nil {
			return request{}, errors.New("capacity file must contain a JSON object")
		}
		return request{method: http.MethodPut, path: "capacity", body: json.RawMessage(body)}, nil
	default:
		return request{}, fmt.Errorf("unknown capacity command %q (see --help)", args[0])
	}
}

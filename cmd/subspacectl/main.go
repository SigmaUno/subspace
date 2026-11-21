package main

import (
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
)

const usage = `Usage: subspacectl [GLOBAL OPTIONS] COMMAND [OPTIONS]

Credentials: --token-file FILE, SUBSPACE_OWNER_TOKEN_FILE, SUBSPACE_OWNER_TOKEN or
--token TOKEN, in that order of preference for files over values. The server URL
comes from --url or SUBSPACE_URL (default http://localhost:8080).

Global options:
  --json         compact JSON on stdout; errors as JSON on stderr; text results as {"text":...}
  --url URL      Subspace base URL
  --token-file F read the owner token from a file
  --token TOKEN  owner token (visible in the process list; prefer a file)

Output is the server's JSON response, indented. Text responses (markdown, NDJSON)
are written as-is unless --json is set.
Exit codes: 0 ok, 1 failure, 2 usage or bad request, 3 credentials rejected or missing,
4 not found, 5 conflict, 6 server unreachable.

Commands:
  create-brief --title TITLE (--body TEXT | --body-file FILE)
               [--repos a/b,c/d] [--allowed-providers openai,deepseek]
               [--max-cost-eur 50] [--max-parallel-agents 10]
  briefs [--status open|closed] [--limit 1000]
  briefs import (--file FILE | --file -) [--dry-run]
  briefs templates [TEMPLATE_ID [--var name=value ...]]
  board BRIEF_ID
  decisions list [--limit N]
  decisions next
  decisions act DECISION_ID --action ACTION [--answer T] [--note T] [--reason T]
                [--status ready|assigned] [--snooze 1h]
  chat send --to agent:ID|brief:ID|leader (--body TEXT | --body-file FILE)
  chat list
  chat show CONVERSATION_ID
  search QUERY [--kind task|message|agent|brief] [--limit N]
  report BRIEF_ID [--format json|md]
  forecast BRIEF_ID
  plans list [--brief ID] [--status STATUS]
  plans show PLAN_ID
  plans approve PLAN_ID [--note TEXT]
  plans reject PLAN_ID --reason TEXT
  plans add-task PLAN_ID --title T (--spec TEXT | --spec-file FILE) --repo R
                 [--role worker|tester] [--criterion TEXT ...] [--depends-on T-1,T-2]
                 [--max-minutes N --max-cost-eur N]
  plans drop-task PLAN_ID TASK_ID
  plans edit-task TASK_ID [--title T] [--spec TEXT | --spec-file FILE] [--repo R]
                  [--role ROLE] [--criterion TEXT ...] [--depends-on T-1,T-2]
                  [--max-minutes N --max-cost-eur N]
  approve-plan PLAN_ID [--note TEXT]
  reject-plan PLAN_ID --reason TEXT
  answer QUESTION_ID --answer TEXT
  tasks list [--brief ID] [--status S] [--assignee A] [--role R] [--limit N]
  tasks show TASK_ID
  tasks pr|scope|gates TASK_ID
  tasks bulk done|cancel|reopen TASK_ID... [--note T] [--reason T] [--status ready|assigned]
  pause-all [--reason TEXT]
  resume-all
  register-agent AGENT_ID (--profile NAME | --role ROLE --kind KIND --model MODEL)
                 [--provider NAME]
  register-agents --prefix PREFIX --count N (--profile NAME | --role ROLE --kind KIND --model MODEL)
                  [--provider NAME]
  revoke-agent AGENT_ID [--reason TEXT]
  agents [--role ROLE] [--status STATUS] [--limit 1000]
  agents stats
  agents show AGENT_ID
  agents rotate-token AGENT_ID [--expires-in 720h]
  agents performance [AGENT_ID] [--role ROLE]
  agents bulk-action pause|resume|revoke|kill AGENT_ID... [--reason TEXT]
  agents register [AGENT_ID | --prefix PREFIX [--count N]] [--profile NAME] [--role ROLE]
                  [--kind KIND] [--model MODEL] [--provider NAME]
  profiles list
  profiles create NAME --kind KIND [--role ROLE] [--model MODEL] [--provider NAME]
                  [--expires-in 720h] [--count N] [--callback-url URL] [--description TEXT]
  profiles show NAME
  profiles update NAME --kind KIND [--role ROLE] [--model MODEL] [--provider NAME]
                  [--expires-in 720h] [--count N] [--callback-url URL] [--description TEXT]
  profiles delete NAME
  repos list
  repos show OWNER/NAME
  repos health OWNER/NAME
  repos remove OWNER/NAME
  repos add OWNER/NAME --host github|forgejo [--base-url URL] [--default-branch BRANCH]
            [--protect PATTERN ...] [--merge-test "make test"] [--merge-workdir DIR]
            [--token-env VAR] [--disabled]
  repos update OWNER/NAME [--host H] [--base-url URL] [--default-branch BRANCH] [--protect PATTERN ...]
               [--merge-test CMD] [--merge-workdir DIR] [--token-env VAR] [--enabled=false]
  spawns list [--brief ID]
  spawns stop SPAWN_ID [--reason TEXT]
  autopilot status
  autopilot enable BRIEF_ID
  autopilot disable BRIEF_ID
  mergequeue list [--repo R]
  mergequeue show CANDIDATE_ID
  mergequeue trains [--repo R] [--limit N]
  mergequeue train TRAIN_ID
  mergequeue enqueue TASK_ID [--pr N] [--base BRANCH]
  backups list
  backups create
  audit list [--agent A] [--task T] [--tool TOOL] [--since ID] [--limit N]
  audit verify
  audit export [--since ID] [--out FILE]
  artifacts list [--scope SCOPE] [--limit N]
  artifacts upload --scope agent:ID|task:ID|project:ID --file FILE [--name N] [--content-type T]
  artifacts download ARTIFACT_ID --out FILE
  artifacts delete ARTIFACT_ID
  instructions get [--scope global|project:ID --kind rules|style|templates | --project ID]
  instructions set --kind KIND (--text TEXT | --text-file FILE) [--scope global] [--message M]
  prompts list [--tag TAG]
  prompts show PROMPT_ID
  prompts render PROMPT_ID [--var name=value ...]
  limits [--provider P] [--model M]
  capacity get
  capacity set --file FILE
  spend-limits get
  spend-limits set --daily-eur N --weekly-eur N          (0 = off)
  quotas show AGENT_ID
  quotas set AGENT_ID --max-cost-eur-per-day N --max-tasks-per-day N --max-concurrent-tasks N
  quotas bulk AGENT_ID... --max-cost-eur-per-day N --max-tasks-per-day N --max-concurrent-tasks N
  schedules list
  schedules show|enable|disable|delete SCHEDULE_ID
  schedules create --file FILE
  schedules edit SCHEDULE_ID --file FILE
  schedules runs SCHEDULE_ID [--limit N]
  schedules preview --cron EXPR [--timezone TZ] [--count N]
  incident list [--limit N]
  incident start --reason TEXT --actions pause_all,revoke_agents,rotate_leader_tokens,snapshot,notify
                 [--revoke-role ROLE] [--revoke-provider NAME]
  incident end INCIDENT_ID [--resume-agents] [--note TEXT]
  drills list [--limit N]
  drills run
  costs [--group-by DIM] [--split-by DIM] [--since TIME] [--until TIME] [--brief ID]
  estimates brief BRIEF_ID
  estimates plan PLAN_ID
  knowledge list [--brief ID] [--kind K] [--status S] [--q TEXT] [--limit N]
  knowledge show|delete|approve ENTRY_ID
  knowledge add --brief ID --kind K --title T (--body TEXT | --body-file FILE)
                [--tags a,b] [--supersedes ENTRY_ID]
  knowledge edit ENTRY_ID [--kind K] [--title T] [--body TEXT | --body-file FILE] [--tags a,b]
  knowledge pin ENTRY_ID [--off]
  knowledge supersede ENTRY_ID [--by ENTRY_ID]
  announcements list [--status active|all]
  announcements publish (--text TEXT | --text-file FILE) [--expires-at TIME] [--brief ID]
  announcements delete ANNOUNCEMENT_ID
  retention status|run|verify
  retention restore SEGMENT_ID
  alerts [--status active|resolved] [--limit N]
  webhooks-out list
  webhooks-out show|delete HOOK_ID
  webhooks-out create --target-url URL --events a.b,c.* [--brief ID]
  webhooks-out deliveries HOOK_ID [--state pending|delivered|dead] [--limit N]
  webhooks-out redeliver HOOK_ID DELIVERY_ID
  breakers list
  breakers reset PROVIDER
  tokens list
  tokens create --name NAME --scope viewer|owner [--expires-at TIME]
  tokens revoke TOKEN_ID
  insights [AGENT_ID | --severity warning|high]
  replay show BRIEF_ID [--at AUDIT_ID|TIME]
  replay timeline BRIEF_ID
  replay diff BRIEF_ID [--from AUDIT_ID|TIME] [--to AUDIT_ID|TIME]
  output tail (AGENT_ID... | --all) [--follow|-f] [--lines N] [--task TASK_ID]
  output latest
  charter get
  charter set (--text TEXT | --text-file FILE)
  notify status|test
  digest [--window 24h]
  logs [--level LEVEL] [--since ID] [--limit N]
  questions [--status open|answered] [--limit N]
  state
  me
  task-templates
  reload
  openapi [--out FILE]
  doctor
  join-snippet AGENT_ID
  completion bash|zsh|fish

doctor runs the server's local diagnostics against this machine's environment and
needs no credentials; it exits 1 when it finds errors. incident start, incident end, drills run and
retention verify print the result and exit 1 when an action failed. output tail --follow
streams until interrupted.

Use "-" for stdin with --body-file, --text-file, --spec-file or --file, and for
--out to write raw data to stdout. Registration results contain one-time
credentials. join-snippet returns configuration with token placeholders.
`

type command func(ctx context.Context, c *conn, p *printer) error

type parser func(args []string, stdin io.Reader) (command, error)

type entry struct {
	parse parser
	subs  []string
	local bool
}

func main() {
	os.Exit(execute())
}

func execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, render(err))
		return exitCode(err)
	}
	return 0
}

func run(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout io.Writer) error {
	opts, args, err := globals(args)
	if err != nil {
		return finish(failf(exitUsage, "%s", err), opts.json)
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(stdout, usage)
		return err
	}
	cmd, err := parse(args, stdin)
	if errors.Is(err, flag.ErrHelp) {
		_, err := io.WriteString(stdout, usage)
		return err
	}
	if err != nil {
		return finish(failf(exitUsage, "%s", err), opts.json)
	}
	var c *conn
	if !registry()[args[0]].local {
		if c, err = connect(opts, getenv); err != nil {
			return finish(err, opts.json)
		}
	}
	return finish(cmd(ctx, c, &printer{w: stdout, json: opts.json}), opts.json)
}

func parse(args []string, stdin io.Reader) (command, error) {
	e, ok := registry()[args[0]]
	if !ok {
		return nil, fmt.Errorf("unknown command %q (see --help)", args[0])
	}
	return e.parse(args[1:], stdin)
}

func single(req request, err error) (command, error) {
	if err != nil {
		return nil, err
	}
	return call(req), nil
}

func byID(name, path string) parser {
	return func(args []string, _ io.Reader) (command, error) {
		id, err := parseID(name, args)
		if err != nil {
			return nil, err
		}
		return call(request{method: http.MethodGet, path: fmt.Sprintf(path, id)}), nil
	}
}

func action(name string) parser {
	return func(args []string, _ io.Reader) (command, error) {
		return single(parseAction(name, args))
	}
}

func registry() map[string]entry {
	return map[string]entry{
		"create-brief": {parse: func(args []string, stdin io.Reader) (command, error) { return single(parseBrief(args, stdin)) }},
		"briefs":       {parse: parseBriefs, subs: []string{"import", "templates"}},
		"board":        {parse: byID("board", "briefs/%s/board")},
		"decisions":    {parse: grouped("decisions", decisionCommands), subs: names(decisionCommands)},
		"chat":         {parse: grouped("chat", chatCommands), subs: names(chatCommands)},
		"search":       {parse: parseSearch},
		"report":       {parse: parseReport},
		"forecast":     {parse: byID("forecast", "briefs/%s/forecast")},
		"plans":        {parse: grouped("plans", planCommands), subs: names(planCommands)},
		"approve-plan": {parse: action("approve-plan")},
		"reject-plan":  {parse: action("reject-plan")},
		"answer":       {parse: action("answer")},
		"tasks":        {parse: grouped("tasks", taskCommands), subs: names(taskCommands)},
		"pause-all":    {parse: action("pause-all")},
		"resume-all":   {parse: action("resume-all")},
		"register-agent": {parse: func(args []string, _ io.Reader) (command, error) {
			return single(parseRegistration("register-agent", args))
		}},
		"register-agents": {parse: func(args []string, _ io.Reader) (command, error) {
			return single(parseRegistration("register-agents", args))
		}},
		"revoke-agent": {parse: action("revoke-agent")},
		"agents":       {parse: parseAgents, subs: names(agentCommands)},
		"profiles":     {parse: grouped("profiles", profileCommands), subs: names(profileCommands)},
		"repos":        {parse: grouped("repos", repoCommands), subs: names(repoCommands)},
		"playbooks":    {parse: grouped("playbooks", playbookCommands), subs: names(playbookCommands)},
		"ha":           {parse: grouped("ha", haCommands), subs: names(haCommands)},
		"spawns":       {parse: grouped("spawns", spawnCommands), subs: names(spawnCommands)},
		"autopilot":    {parse: grouped("autopilot", autopilotCommands), subs: names(autopilotCommands)},
		"mergequeue":   {parse: grouped("mergequeue", mergequeueCommands), subs: names(mergequeueCommands)},
		"backups":      {parse: grouped("backups", backupCommands), subs: names(backupCommands)},
		"audit":        {parse: grouped("audit", auditCommands), subs: names(auditCommands)},
		"artifacts":    {parse: grouped("artifacts", artifactCommands), subs: names(artifactCommands)},
		"instructions": {parse: grouped("instructions", instructionCommands), subs: names(instructionCommands)},
		"prompts":      {parse: grouped("prompts", promptCommands), subs: names(promptCommands)},
		"limits":       {parse: parseLimits},
		"capacity": {parse: func(args []string, stdin io.Reader) (command, error) {
			return single(parseCapacity(args, stdin))
		}, subs: []string{"get", "set"}},
		"join-snippet":   {parse: byID("join-snippet", "agents/%s/connect")},
		"completion":     {parse: parseCompletion, subs: []string{"bash", "zsh", "fish"}, local: true},
		"schedules":      {parse: grouped("schedules", scheduleCommands), subs: names(scheduleCommands)},
		"incident":       {parse: grouped("incident", incidentCommands), subs: names(incidentCommands)},
		"drills":         {parse: grouped("drills", drillCommands), subs: names(drillCommands)},
		"costs":          {parse: parseCosts},
		"estimates":      {parse: grouped("estimates", estimateCommands), subs: names(estimateCommands)},
		"knowledge":      {parse: grouped("knowledge", knowledgeCommands), subs: names(knowledgeCommands)},
		"announcements":  {parse: grouped("announcements", announcementCommands), subs: names(announcementCommands)},
		"quotas":         {parse: grouped("quotas", quotaCommands), subs: names(quotaCommands)},
		"retention":      {parse: grouped("retention", retentionCommands), subs: names(retentionCommands)},
		"alerts":         {parse: listing("alerts", "alerts", 1000, "status")},
		"webhooks-out":   {parse: grouped("webhooks-out", webhookCommands), subs: names(webhookCommands)},
		"breakers":       {parse: grouped("breakers", breakerCommands), subs: names(breakerCommands)},
		"spend-limits":   {parse: grouped("spend-limits", spendLimitCommands), subs: names(spendLimitCommands)},
		"tokens":         {parse: grouped("tokens", tokenCommands), subs: names(tokenCommands)},
		"insights":       {parse: parseInsights},
		"replay":         {parse: grouped("replay", replayCommands), subs: names(replayCommands)},
		"reload":         {parse: plain("reload", request{method: http.MethodPost, path: "reload", body: struct{}{}})},
		"doctor":         {parse: parseDoctor, local: true},
		"openapi":        {parse: parseOpenAPI},
		"output":         {parse: grouped("output", outputCommands), subs: names(outputCommands)},
		"charter":        {parse: grouped("charter", charterCommands), subs: names(charterCommands)},
		"notify":         {parse: grouped("notify", notifyCommands), subs: names(notifyCommands)},
		"digest":         {parse: parseDigest},
		"logs":           {parse: parseLogs},
		"questions":      {parse: listing("questions", "questions", 1000, "status")},
		"state":          {parse: plain("state", request{method: http.MethodGet, path: "state"})},
		"me":             {parse: plain("me", request{method: http.MethodGet, path: "me"})},
		"task-templates": {parse: plain("task-templates", request{method: http.MethodGet, path: "task-templates"})},
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
		if f := set.Lookup(name); f != nil && !hasValue {
			if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
				continue
			}
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
	return os.ReadFile(path) //nolint:gosec
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
	profile := set.String("profile", "", "agent profile supplying the fields left out")
	var id, prefix string
	var count int
	if command != "register-agent" {
		set.StringVar(&prefix, "prefix", "", "agent ID prefix")
		set.IntVar(&count, "count", 0, "number of agents to register")
	}
	if command != "register-agents" {
		set.StringVar(&id, "id", "", "agent ID (alternative to positional argument)")
	}
	maxArgs := 1
	if command == "register-agents" {
		maxArgs = 0
	}
	if err := parseFlags(set, args, 0, maxArgs); err != nil {
		return request{}, err
	}
	if *profile == "" && (strings.TrimSpace(*role) == "" || strings.TrimSpace(*kind) == "" || strings.TrimSpace(*model) == "") {
		return request{}, fmt.Errorf("%s requires --role, --kind and --model, or --profile", command)
	}
	body := map[string]any{"role": *role, "kind": *kind, "model": *model, "provider": *provider}
	if *profile != "" {
		name, err := identifier(*profile)
		if err != nil {
			return request{}, err
		}
		body["profile"] = name
	}
	path := "agents"
	if command == "register-agents" || prefix != "" {
		if set.NArg() != 0 || id != "" {
			return request{}, errors.New("specify either an agent ID or --prefix")
		}
		if _, err := identifier(prefix); err != nil || count > 1000 || count < 0 || (count == 0 && *profile == "") {
			return request{}, errors.New("bulk registration requires a valid --prefix and --count in 1..1000 (--count may be left out with --profile)")
		}
		path = "agents/bulk"
		body["prefix"] = prefix
		if count > 0 {
			body["count"] = count
		}
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

package acceptance

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/SigmaUno/subspace/internal/agentout"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/model"
)

type scenario struct {
	askTitle    string
	defectTitle string
	answer      string
}

type agentEnv struct {
	url    string
	world  *world
	bus    *bus
	forge  *forge
	git    *gitRepo
	ledger *ledger
	sc     scenario
}

type scripted struct {
	env     *agentEnv
	id      string
	mcp     *client.MCP
	handled map[string]bool
	prs     map[string]int
	spent   map[string]float64
	answers []string
}

func newScripted(ctx context.Context, env *agentEnv, id, token string) (*scripted, error) {
	m := client.NewMCP(env.url+"/mcp", token, client.WithClientName(id))
	if err := m.Initialize(ctx); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if _, err := m.WhoAmI(ctx); err != nil {
		return nil, fmt.Errorf("whoami: %w", err)
	}
	if _, err := m.GetInstructions(ctx); err != nil {
		return nil, fmt.Errorf("get_instructions: %w", err)
	}
	return &scripted{env: env, id: id, mcp: m, handled: map[string]bool{}, prs: map[string]int{}, spent: map[string]float64{}}, nil
}

func (s *scripted) run(ctx context.Context) error {
	defer func() { _ = s.mcp.Close(context.WithoutCancel(ctx)) }()
	for {
		ch := s.env.bus.wait()
		for _, t := range s.env.world.view().ready(s.id) {
			if err := s.work(ctx, t); err != nil {
				return fmt.Errorf("%s: %w", t.ID, err)
			}
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return nil
		}
	}
}

func taskNumber(id string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(id, "T-"))
	return n
}

func (s *scripted) work(ctx context.Context, t *model.Task) error {
	key := fmt.Sprintf("%s#%d", t.ID, t.ReviewRounds)
	if s.handled[key] {
		return nil
	}
	s.handled[key] = true
	round, n := t.ReviewRounds, taskNumber(t.ID)
	if _, err := s.mcp.ClaimTask(ctx, t.ID); err != nil {
		return fmt.Errorf("claim_task: %w", err)
	}
	if round == 0 && t.Title == s.env.sc.askTitle {
		if err := s.askOwner(ctx, t); err != nil {
			return err
		}
	}
	if round > 0 {
		if err := s.readFeedback(ctx, t); err != nil {
			return err
		}
	}
	if err := s.progress(ctx, t, 25, "started"); err != nil {
		return err
	}
	for _, line := range []string{"go build ./...", "go test ./...", "ok  \t" + t.Branch} {
		if _, err := s.mcp.LogOutput(ctx, agentout.Input{TaskID: t.ID, Stream: "stdout", Text: line}); err != nil {
			return fmt.Errorf("log_output: %w", err)
		}
	}
	if hb, err := s.mcp.Heartbeat(ctx, t.ID); err != nil || !hb.OK || hb.Paused {
		return fmt.Errorf("heartbeat: %+v %w", hb, err)
	}
	file := fmt.Sprintf("%s.txt", strings.ToLower(t.ID))
	if t.Role == model.RoleWorker {
		if err := s.env.git.push(t.Branch, file, fmt.Sprintf("%s round %d\n", t.ID, round)); err != nil {
			return err
		}
	}
	pr := s.openOrFix(t, file)
	line := usageLine{agent: s.id, task: t.ID, in: int64(1000 * n), out: int64(200 * n), usd: 0.25 * float64(n)}
	if _, err := s.mcp.ReportUsage(ctx, client.UsageReport{
		Provider: "anthropic", Model: "claude-sonnet-5-5", TokensIn: line.in, TokensOut: line.out, CostUSD: line.usd,
		TaskID: t.ID, RequestID: fmt.Sprintf("%s-%s-%d", s.id, t.ID, round),
	}); err != nil {
		return fmt.Errorf("report_usage: %w", err)
	}
	s.env.ledger.add(line)
	s.spent[t.ID] += line.usd
	if err := s.progress(ctx, t, 90, "ready for review"); err != nil {
		return err
	}
	if t.Role == model.RoleTester {
		if _, err := s.mcp.ReportTests(ctx, t.ID, 12, 0, ""); err != nil {
			return fmt.Errorf("report_tests: %w", err)
		}
	}
	if _, err := s.mcp.Submit(ctx, board.SubmitInput{TaskID: t.ID, PRURL: s.env.forge.prURL(pr), Summary: "done"}); err != nil {
		return fmt.Errorf("submit: %w", err)
	}
	return nil
}

func (s *scripted) progress(ctx context.Context, t *model.Task, pct int, note string) error {
	cost := s.spent[t.ID]
	_, err := s.mcp.UpdateStatus(ctx, board.StatusInput{TaskID: t.ID, Status: model.StatusInProgress, ProgressPct: &pct, Note: note, CostEUR: &cost})
	if err != nil {
		return fmt.Errorf("update_status: %w", err)
	}
	return nil
}

func (s *scripted) openOrFix(t *model.Task, file string) int {
	if n, ok := s.prs[t.ID]; ok {
		s.env.forge.repoint(n, t.Branch)
		return n
	}
	branch := t.Branch
	if t.Title == s.env.sc.defectTitle {
		branch = "wip/" + branch
	}
	n := s.env.forge.open(t.Title, branch, []string{file})
	s.prs[t.ID] = n
	return n
}

func (s *scripted) askOwner(ctx context.Context, t *model.Task) error {
	q, err := s.mcp.AskOwner(ctx, t.ID, "Which money format should the API return?", "cents", "decimal")
	if err != nil {
		return fmt.Errorf("ask_owner: %w", err)
	}
	err = s.env.bus.until(ctx, func() bool {
		got := s.env.world.view().questions[q.ID]
		return got != nil && got.Status == "answered"
	})
	if err != nil {
		return fmt.Errorf("waiting for the answer to %s: %w", q.ID, err)
	}
	msgs, err := s.mcp.ReadThread(ctx, "task:"+t.ID, 0, 50)
	if err != nil {
		return fmt.Errorf("read_thread: %w", err)
	}
	for _, m := range msgs {
		if m.Kind == model.MsgAnswer && strings.Contains(m.Body, s.env.sc.answer) {
			s.answers = append(s.answers, m.Body)
			_, err := s.mcp.AddNote(ctx, t.ID, "owner answered: "+s.env.sc.answer)
			return err
		}
	}
	return fmt.Errorf("the owner's answer to %s is not in the task thread", q.ID)
}

func (s *scripted) readFeedback(ctx context.Context, t *model.Task) error {
	msgs, err := s.mcp.ReadThread(ctx, "task:"+t.ID, 0, 100)
	if err != nil {
		return fmt.Errorf("read_thread: %w", err)
	}
	for _, m := range msgs {
		if strings.Contains(m.Body, "not the task branch") {
			return nil
		}
	}
	return fmt.Errorf("no reviewer feedback about the branch in the thread of %s", t.ID)
}

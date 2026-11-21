package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/SigmaUno/subspace/internal/autopilot"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/briefs"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/estimate"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/profiles"
	"github.com/SigmaUno/subspace/internal/repos"
	"github.com/SigmaUno/subspace/internal/spendguard"
)

const (
	markCreate  = "+"
	markUpdate  = "~"
	markSame    = "="
	markInfo    = "-"
	markWarn    = "?"
	markProblem = "!"
	fileHeader  = "# Subspace join lines and MCP snippets of new agents. Keep this file private.\n"
)

type bootstrap struct {
	o       *client.Owner
	cfg     *Config
	dry     bool
	approve bool
	show    bool
	out     io.Writer

	problems int
	secrets  *os.File
	issued   int
}

func (b *bootstrap) say(mark, format string, args ...any) {
	if mark == markProblem {
		b.problems++
	}
	_, _ = fmt.Fprintf(b.out, "%s %s\n", mark, fmt.Sprintf(format, args...))
}

func (b *bootstrap) change(mark, format string, args ...any) {
	if b.dry {
		format = "would " + format
	}
	b.say(mark, format, args...)
}

func (b *bootstrap) get(ctx context.Context, op, path string, out any) (bool, error) {
	err := b.o.Do(ctx, op, "GET", path, nil, out)
	if client.IsCode(err, client.CodeNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (b *bootstrap) run(ctx context.Context) error {
	if b.dry {
		b.say(markInfo, "dry run: nothing is changed")
	}
	if _, err := b.o.Health(ctx); err != nil {
		return fmt.Errorf("server not reachable: %w", err)
	}
	if err := b.o.Do(ctx, "me", "GET", "/api/v1/me", nil, nil); err != nil {
		return fmt.Errorf("owner token rejected: %w", err)
	}
	doc, err := b.cfg.loadBrief()
	if err != nil {
		return err
	}
	if err := b.checkProviders(ctx); err != nil {
		return err
	}
	defer b.closeSecrets()
	steps := []func(context.Context) error{b.charter, b.profiles, b.repos, b.capacity, b.limits, b.agents}
	for _, step := range steps {
		if err := step(ctx); err != nil {
			return err
		}
	}
	brief, err := b.brief(ctx, doc)
	if err != nil {
		return err
	}
	if err := b.gates(ctx, doc, brief); err != nil {
		return err
	}
	if err := b.autopilot(ctx, brief); err != nil {
		return err
	}
	if err := b.plan(ctx, brief); err != nil {
		return err
	}
	if b.issued > 0 {
		b.say(markInfo, "join lines and MCP snippets for %d new agents written to %s (mode 0600)", b.issued, b.cfg.secretsPath())
	}
	if b.problems > 0 {
		return fmt.Errorf("%d problems need attention", b.problems)
	}
	return nil
}

func (b *bootstrap) charter(ctx context.Context) error {
	if b.cfg.Charter == nil {
		return nil
	}
	text, err := b.cfg.renderCharter()
	if err != nil {
		return fmt.Errorf("charter: %w", err)
	}
	cur, err := b.o.Charter(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cur.Text) == strings.TrimSpace(text) {
		b.say(markSame, "charter")
		return nil
	}
	b.change(markUpdate, "set charter (%d bytes)", len(text))
	if b.dry {
		return nil
	}
	_, err = b.o.SetCharter(ctx, text)
	return err
}

func (b *bootstrap) profiles(ctx context.Context) error {
	for _, want := range b.cfg.Profiles {
		path := "/api/v1/agent-profiles/" + url.PathEscape(want.Name)
		var cur profiles.Profile
		found, err := b.get(ctx, "get-profile", path, &cur)
		if err != nil {
			return err
		}
		switch {
		case !found:
			b.change(markCreate, "create profile %s", want.Name)
			if !b.dry {
				err = b.o.Do(ctx, "create-profile", "POST", "/api/v1/agent-profiles", want, nil)
			}
		case sameProfile(cur, want):
			b.say(markSame, "profile %s", want.Name)
		default:
			b.change(markUpdate, "update profile %s", want.Name)
			if !b.dry {
				err = b.o.Do(ctx, "update-profile", "PUT", path, want, nil)
			}
		}
		if err != nil {
			return fmt.Errorf("profile %s: %w", want.Name, err)
		}
	}
	return nil
}

func sameProfile(cur, want profiles.Profile) bool {
	return cur.Role == cmp.Or(want.Role, model.RoleWorker) && cur.Kind == want.Kind && cur.Model == want.Model &&
		(want.Provider == "" || cur.Provider == want.Provider) && cur.ExpiresIn == want.ExpiresIn && cur.Count == want.Count &&
		cur.CallbackURL == want.CallbackURL && cur.Description == want.Description
}

func (b *bootstrap) repos(ctx context.Context) error {
	for _, want := range b.cfg.Repos {
		path := "/api/v1/repos/" + want.Name
		var cur repos.Repo
		found, err := b.get(ctx, "get-repo", path, &cur)
		if err != nil {
			return err
		}
		switch {
		case !found:
			b.change(markCreate, "register repo %s (%s)", want.Name, want.Host)
			if !b.dry {
				err = b.o.Do(ctx, "create-repo", "POST", "/api/v1/repos", want, nil)
			}
		case sameRepo(cur, want):
			b.say(markSame, "repo %s", want.Name)
		default:
			b.change(markUpdate, "update repo %s", want.Name)
			if !b.dry {
				err = b.o.Do(ctx, "update-repo", "PUT", path, want, nil)
			}
		}
		if err != nil {
			return fmt.Errorf("repo %s: %w", want.Name, err)
		}
	}
	return nil
}

func sameRepo(cur repos.Repo, want repos.Input) bool {
	enabled := want.Enabled == nil || *want.Enabled
	return cur.Host == want.Host && (want.BaseURL == "" || cur.BaseURL == want.BaseURL) &&
		(want.DefaultBranch == "" || cur.DefaultBranch == want.DefaultBranch) &&
		slices.Equal(cur.ProtectedBranches, want.ProtectedBranches) && slices.Equal(cur.MergeTestCommand, want.MergeTestCommand) &&
		cur.MergeWorkDir == want.MergeWorkDir && cur.TokenEnv == want.TokenEnv && cur.Enabled == enabled
}

func (b *bootstrap) capacity(ctx context.Context) error {
	want := b.cfg.Capacity
	if want == nil {
		return nil
	}
	var cur capacity.Config
	if err := b.o.Do(ctx, "capacity", "GET", "/api/v1/capacity", nil, &cur); err != nil {
		return err
	}
	order := func(a, c capacity.Limit) int {
		return cmp.Or(strings.Compare(a.Provider, c.Provider), strings.Compare(a.Model, c.Model))
	}
	have, need := slices.Clone(cur.Limits), slices.Clone(want.Limits)
	slices.SortFunc(have, order)
	slices.SortFunc(need, order)
	if cur.GlobalMaxAgents == want.GlobalMaxAgents && slices.Equal(have, need) {
		b.say(markSame, "capacity")
		return nil
	}
	b.change(markUpdate, "set capacity (global %d, %d limits)", want.GlobalMaxAgents, len(want.Limits))
	if b.dry {
		return nil
	}
	_, err := b.o.SetCapacity(ctx, *want)
	return err
}

func (b *bootstrap) limits(ctx context.Context) error {
	want := b.cfg.Limits
	if want == nil {
		return nil
	}
	var cur spendguard.Limits
	if err := b.o.Do(ctx, "limits", "GET", "/api/v1/limits", nil, &cur); err != nil {
		return err
	}
	if cur == *want {
		b.say(markSame, "org spend caps")
		return nil
	}
	b.change(markUpdate, "set org spend caps (daily %.2f EUR, weekly %.2f EUR)", want.DailyCostEUR, want.WeeklyCostEUR)
	if b.dry {
		return nil
	}
	return b.o.Do(ctx, "set-limits", "PUT", "/api/v1/limits", want, nil)
}

func (b *bootstrap) profile(ctx context.Context, name string) (*profiles.Profile, error) {
	for i := range b.cfg.Profiles {
		if b.cfg.Profiles[i].Name == name {
			return &b.cfg.Profiles[i], nil
		}
	}
	var p profiles.Profile
	found, err := b.get(ctx, "get-profile", "/api/v1/agent-profiles/"+url.PathEscape(name), &p)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("unknown profile %q", name)
	}
	return &p, nil
}

func (b *bootstrap) provider(ctx context.Context, a agentPlan) (string, error) {
	if a.Provider != "" {
		return a.Provider, nil
	}
	kind := a.Kind
	if a.Profile != "" {
		p, err := b.profile(ctx, a.Profile)
		if err != nil {
			return "", fmt.Errorf("%s %s: %w", a.Role, a.ID, err)
		}
		if p.Provider != "" && (kind == "" || kind == p.Kind) {
			return p.Provider, nil
		}
		kind = cmp.Or(kind, p.Kind)
	}
	return model.ProviderForKind(kind), nil
}

func (b *bootstrap) checkProviders(ctx context.Context) error {
	reviewer := agentPlan{b.cfg.Reviewer, model.RoleReviewer}
	rp, err := b.provider(ctx, reviewer)
	if err != nil {
		return err
	}
	for _, a := range b.cfg.agents() {
		if a.Role != model.RoleWorker {
			continue
		}
		wp, err := b.provider(ctx, a)
		if err != nil {
			return err
		}
		if wp == rp {
			return fmt.Errorf("reviewer %s uses provider %s like worker %s; cross review needs a different provider", reviewer.ID, rp, a.ID)
		}
	}
	return nil
}

func (b *bootstrap) agents(ctx context.Context) error {
	for _, a := range b.cfg.agents() {
		want, err := b.provider(ctx, a)
		if err != nil {
			return err
		}
		var cur model.Agent
		found, err := b.get(ctx, "get-agent", "/api/v1/agents/"+a.ID, &cur)
		if err != nil {
			return err
		}
		switch {
		case !found:
			b.change(markCreate, "register %s %s (%s)", a.Role, a.ID, want)
			if !b.dry {
				err = b.register(ctx, a)
			}
		case cur.Role != a.Role:
			b.say(markProblem, "agent %s exists with role %s, want %s", a.ID, cur.Role, a.Role)
		case cur.Status == model.AgentRevoked:
			b.say(markProblem, "agent %s is revoked; pick a new id or reissue it", a.ID)
		case cur.Provider != want:
			b.say(markProblem, "agent %s exists with provider %s, want %s", a.ID, cur.Provider, want)
		default:
			b.say(markSame, "%s %s", a.Role, a.ID)
		}
		if err != nil {
			return fmt.Errorf("register %s: %w", a.ID, err)
		}
	}
	return nil
}

func (b *bootstrap) register(ctx context.Context, a agentPlan) error {
	if err := b.openSecrets(); err != nil {
		return err
	}
	body := map[string]any{"id": a.ID, "role": a.Role}
	for k, v := range map[string]string{"kind": a.Kind, "model": a.Model, "provider": a.Provider, "profile": a.Profile, "expires_in": a.ExpiresIn} {
		if v != "" {
			body[k] = v
		}
	}
	var reg board.Registered
	if err := b.o.Do(ctx, "register-agent", "POST", "/api/v1/agents", body, &reg); err != nil {
		return err
	}
	return b.writeSecret(reg)
}

func (b *bootstrap) openSecrets() error {
	if b.secrets != nil {
		return nil
	}
	path := b.cfg.secretsPath()
	f, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("secrets file: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("secrets file: %w", err)
	}
	if st, err := f.Stat(); err == nil && st.Size() == 0 {
		_, err = f.WriteString(fileHeader)
		if err != nil {
			_ = f.Close()
			return fmt.Errorf("secrets file: %w", err)
		}
	}
	b.secrets = f
	return nil
}

func (b *bootstrap) closeSecrets() {
	if b.secrets != nil {
		_ = b.secrets.Close()
	}
}

func (b *bootstrap) writeSecret(reg board.Registered) error {
	var s strings.Builder
	fmt.Fprintf(&s, "\n## %s role=%s kind=%s provider=%s\n", reg.Agent.ID, reg.Agent.Role, reg.Agent.Kind, reg.Agent.Provider)
	fmt.Fprintf(&s, "join_line: %s\nmcp_url: %s\ntoken: %s\n", reg.Connect.JoinLine, reg.Connect.MCPURL, reg.Connect.Token)
	names := make([]string, 0, len(reg.Connect.Snippets))
	for k := range reg.Connect.Snippets {
		names = append(names, k)
	}
	slices.Sort(names)
	for _, k := range names {
		fmt.Fprintf(&s, "%s:\n%s\n", k, strings.TrimRight(reg.Connect.Snippets[k], "\n"))
	}
	if _, err := b.secrets.WriteString(s.String()); err != nil {
		return fmt.Errorf("secrets file: %w; rotate the token of %s", err, reg.Agent.ID)
	}
	if err := b.secrets.Sync(); err != nil {
		return fmt.Errorf("secrets file: %w", err)
	}
	b.issued++
	if b.show {
		_, _ = io.WriteString(b.out, s.String())
	}
	return nil
}

func (b *bootstrap) brief(ctx context.Context, doc *briefs.Doc) (*model.Brief, error) {
	all, err := b.o.ListBriefs(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, br := range all {
		if br.Title == doc.Title {
			b.say(markSame, "brief %s %q (%s)", br.ID, br.Title, br.Status)
			if br.Status != "open" {
				b.say(markWarn, "brief %s is %s", br.ID, br.Status)
			}
			return br, nil
		}
	}
	b.change(markCreate, "create brief %q (%d repos, max %.2f EUR)", doc.Title, len(doc.Repos), doc.MaxCostEUR)
	if b.dry {
		return nil, nil
	}
	raw, err := os.ReadFile(b.cfg.path(b.cfg.Brief))
	if err != nil {
		return nil, err
	}
	var out struct {
		Brief *model.Brief `json:"brief"`
	}
	if err := b.o.Do(ctx, "import-brief", "POST", "/api/v1/briefs/import", map[string]string{"markdown": string(raw)}, &out); err != nil {
		return nil, fmt.Errorf("create brief: %w", err)
	}
	if out.Brief == nil {
		return nil, errors.New("create brief: empty response")
	}
	b.say(markInfo, "brief %s created", out.Brief.ID)
	return out.Brief, nil
}

func (b *bootstrap) gates(ctx context.Context, doc *briefs.Doc, brief *model.Brief) error {
	want := cmp.Or(doc.QualityGates, b.cfg.QualityGates)
	if want == nil {
		return nil
	}
	if brief == nil {
		if doc.QualityGates == nil {
			b.change(markUpdate, "set quality gates on the new brief")
		}
		return nil
	}
	if brief.QualityGates == *want {
		b.say(markSame, "quality gates of %s", brief.ID)
		return nil
	}
	b.change(markUpdate, "set quality gates of %s", brief.ID)
	if b.dry {
		return nil
	}
	_, err := b.o.UpdateBrief(ctx, brief.ID, board.BriefPatch{QualityGates: want})
	return err
}

type autopilotView struct {
	Briefs []struct {
		ID      string                 `json:"id"`
		Enabled bool                   `json:"enabled"`
		Spawn   *autopilot.SpawnConfig `json:"spawn"`
	} `json:"briefs"`
}

func (b *bootstrap) autopilot(ctx context.Context, brief *model.Brief) error {
	want := b.cfg.Autopilot
	if want == nil || !want.Enabled {
		return nil
	}
	var view autopilotView
	found, err := b.get(ctx, "autopilot", "/api/v1/autopilot", &view)
	if err != nil {
		return err
	}
	if !found {
		b.say(markWarn, "autopilot is not running on the server (start it with SUBSPACE_AUTOPILOT=on); skipped")
		return nil
	}
	if brief == nil {
		b.change(markUpdate, "enable autopilot for the new brief")
		return nil
	}
	enabled, spawn := false, autopilot.SpawnConfig{}
	for _, f := range view.Briefs {
		if f.ID == brief.ID {
			enabled = f.Enabled
			if f.Spawn != nil {
				spawn = *f.Spawn
			}
		}
	}
	path := "/api/v1/autopilot/briefs/" + brief.ID
	if enabled {
		b.say(markSame, "autopilot on for %s", brief.ID)
	} else {
		b.change(markUpdate, "enable autopilot for %s", brief.ID)
		if !b.dry {
			if err := b.o.Do(ctx, "autopilot-enable", "POST", path, map[string]bool{"enabled": true}, nil); err != nil {
				return err
			}
		}
	}
	if want.Spawn == nil || *want.Spawn == spawn {
		return nil
	}
	b.change(markUpdate, "set autopilot spawn options for %s", brief.ID)
	if b.dry {
		return nil
	}
	return b.o.Do(ctx, "autopilot-spawn", "PUT", path+"/spawn", want.Spawn, nil)
}

func (b *bootstrap) plan(ctx context.Context, brief *model.Brief) error {
	if brief == nil {
		return nil
	}
	plans, err := b.o.ListPlans(ctx, brief.ID, "")
	if err != nil {
		return err
	}
	var proposed *model.Plan
	for _, p := range plans {
		switch p.Status {
		case "approved":
			b.say(markSame, "plan %s approved (%d tasks)", p.ID, len(p.Tasks))
			return nil
		case "proposed":
			proposed = p
		}
	}
	if proposed == nil {
		b.say(markInfo, "no plan proposed for %s yet; rerun once the leader has proposed one", brief.ID)
		return nil
	}
	if err := b.preview(ctx, proposed); err != nil {
		return err
	}
	if !b.approve {
		b.say(markInfo, "plan %s left for review; rerun with -approve-plan to approve it", proposed.ID)
		return nil
	}
	b.change(markUpdate, "approve plan %s", proposed.ID)
	if b.dry {
		return nil
	}
	_, err = b.o.ApprovePlan(ctx, proposed.ID, "approved by subspace-bootstrap")
	return err
}

func (b *bootstrap) preview(ctx context.Context, p *model.Plan) error {
	b.say(markInfo, "plan %s by %s: %d tasks, %d lint findings", p.ID, p.ProposedBy, len(p.Tasks), len(p.Warnings))
	for _, w := range p.Warnings {
		where := ""
		if w.Task != "" {
			where = " [" + w.Task + "]"
		}
		b.say(markWarn, "  %s %s%s: %s", w.Severity, w.Rule, where, w.Message)
	}
	var est estimate.Report
	if err := b.o.Do(ctx, "plan-estimates", "GET", "/api/v1/plans/"+p.ID+"/estimates", nil, &est); err != nil {
		return err
	}
	if !est.Sufficient {
		b.say(markInfo, "  estimates: not enough history (%d completed tasks)", est.History)
		return nil
	}
	t := est.Totals
	b.say(markInfo, "  estimates: %d/%d tasks, %.0f-%.0f min (expected %.0f), critical path %.0f min, %.2f-%.2f EUR (expected %.2f)",
		t.Estimated, t.Tasks, t.Minutes.Low, t.Minutes.High, t.Minutes.Expected, t.CriticalPathMinutes.Expected,
		t.CostEUR.Low, t.CostEUR.High, t.CostEUR.Expected)
	for _, te := range est.Tasks {
		if len(te.ExceedsBudget) > 0 {
			b.say(markWarn, "  %s %q: estimate exceeds %s", te.TaskID, te.Title, strings.Join(te.ExceedsBudget, ", "))
		}
	}
	return nil
}

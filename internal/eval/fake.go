package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/SigmaUno/subspace/internal/llm"
	"github.com/SigmaUno/subspace/internal/model"
)

var briefIDLine = regexp.MustCompile(`(?m)^- id: (\S+)$`)

func NewFake(set *Set) llm.Provider {
	var n atomic.Int64
	return &llm.Func{ProviderName: "fake", ModelName: "fake-oracle", Respond: func(_ context.Context, req llm.Request) (*llm.Response, error) {
		if len(req.Messages) == 0 {
			return nil, errors.New("fake: no messages")
		}
		user := req.Messages[0].Content
		var text string
		var err error
		if strings.HasPrefix(user, "{") {
			text, err = fakeReview(set, user)
		} else {
			text, err = fakePlan(set, user)
		}
		if err != nil {
			return nil, err
		}
		return &llm.Response{
			ID:    fmt.Sprintf("fake-%d", n.Add(1)),
			Model: "fake-oracle",
			Text:  text,
			Usage: llm.Usage{InputTokens: int64(len(req.System)+len(user)) / 4, OutputTokens: int64(len(text)) / 4},
		}, nil
	}}
}

func fakePlan(set *Set, user string) (string, error) {
	m := briefIDLine.FindStringSubmatch(user)
	if m == nil {
		return "", errors.New("fake: no brief id in the prompt")
	}
	f := set.plan(m[1])
	if f == nil {
		return "", fmt.Errorf("fake: unknown plan fixture %q", m[1])
	}
	cost := 5.0
	if f.MaxCostEUR > 0 {
		cost = min(cost, 0.8*f.MaxCostEUR/float64(len(f.Requirements)))
	}
	out := planOutput{Note: "", Tasks: []planTask{}}
	for _, r := range f.Requirements {
		repo := r.Repo
		if repo == "" {
			repo = f.Repos[0].Name
		}
		role := r.Role
		if role == "" {
			role = model.RoleWorker
		}
		words := make([]string, len(r.Keywords))
		for i, k := range r.Keywords {
			words[i], _, _ = strings.Cut(k, "|")
		}
		criteria := make([]string, 0, len(r.Checks)+1)
		for _, c := range r.Checks {
			first, _, _ := strings.Cut(c, "|")
			criteria = append(criteria, fmt.Sprintf("The change covers %s and go test ./... passes", first))
		}
		criteria = append(criteria, "go build ./... and go vet ./... pass")
		out.Tasks = append(out.Tasks, planTask{
			Ref:                r.ID,
			Title:              r.Title,
			Spec:               fmt.Sprintf("%s in %s. Scope: %s. Verify with tests.", r.Title, repo, strings.Join(words, ", ")),
			AcceptanceCriteria: criteria,
			Repo:               repo,
			DependsOn:          append([]string{}, r.After...),
			Role:               role,
			Budget:             model.Budget{MaxMinutes: 90, MaxCostEUR: float64(int(cost*100)) / 100},
		})
	}
	b, err := json.Marshal(out)
	return string(b), err
}

func fakeReview(set *Set, user string) (string, error) {
	var in reviewInput
	if err := json.Unmarshal([]byte(user), &in); err != nil {
		return "", fmt.Errorf("fake: %w", err)
	}
	f := set.review(in.TaskID)
	if f == nil {
		return "", fmt.Errorf("fake: unknown review fixture %q", in.TaskID)
	}
	out := reviewOutput{Verdict: f.Expected(), Comments: []string{}, PerCriterion: []criterionResult{}}
	byCriterion := map[int][]string{}
	for _, d := range f.Defects {
		msg := fmt.Sprintf("%s:%d: %s: %s", d.File, d.Line, d.Kind, d.Note)
		out.Comments = append(out.Comments, msg)
		byCriterion[d.Criterion] = append(byCriterion[d.Criterion], msg)
	}
	if f.Tests != nil && f.Tests.Failed > 0 {
		out.Comments = append(out.Comments, fmt.Sprintf("%d tests failed", f.Tests.Failed))
	}
	for i, c := range f.AcceptanceCriteria {
		met := len(byCriterion[i]) == 0
		ev := "shown by the diff"
		if !met {
			ev = strings.Join(byCriterion[i], "; ")
		}
		out.PerCriterion = append(out.PerCriterion, criterionResult{Criterion: c, Met: &met, Evidence: ev})
	}
	b, err := json.Marshal(out)
	return string(b), err
}

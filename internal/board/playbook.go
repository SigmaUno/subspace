package board

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/planlint"
	"github.com/SigmaUno/subspace/internal/store"
)

type PlaybookRules func(tx store.Tx, name string) (*planlint.Rules, error)

func (b *Board) SetPlaybooks(r PlaybookRules) {
	b.mu.Lock()
	b.rules = r
	b.mu.Unlock()
}

func (b *Board) rulesFor(tx store.Tx, name string) (*planlint.Rules, error) {
	if name == "" {
		return nil, nil
	}
	b.mu.Lock()
	r := b.rules
	b.mu.Unlock()
	if r == nil {
		return nil, model.BadRequest("playbooks are not available")
	}
	return r(unscoped(tx), name)
}

func (b *Board) briefRules(tx store.Tx, name string) (*planlint.Rules, error) {
	rules, err := b.rulesFor(tx, name)
	if err != nil && model.AsError(err).Code == "not_found" {
		return nil, model.BadRequest("unknown playbook %q", name)
	}
	return rules, err
}

func withDefaultBudgets(rules *planlint.Rules, in []TaskInput) []TaskInput {
	if rules == nil {
		return in
	}
	out := make([]TaskInput, len(in))
	for i, t := range in {
		t.Budget = rules.ApplyDefaults(t.Budget)
		out[i] = t
	}
	return out
}

func (b *Board) playbookInstructions(ctx context.Context, a model.Actor) (string, error) {
	byPlaybook := map[string][]string{}
	text := map[string]string{}
	err := b.readAs(ctx, a, func(tx store.Tx) error {
		bs, err := tx.ListBriefs(store.BriefFilter{Status: "open"})
		if err != nil {
			return err
		}
		for _, br := range bs {
			if br.Playbook == "" {
				continue
			}
			if _, ok := text[br.Playbook]; !ok {
				r, err := b.rulesFor(tx, br.Playbook)
				if err != nil {
					return err
				}
				if r == nil {
					continue
				}
				text[br.Playbook] = r.Instructions
			}
			byPlaybook[br.Playbook] = append(byPlaybook[br.Playbook], br.ID)
		}
		return nil
	})
	if err != nil || len(byPlaybook) == 0 {
		return "", err
	}
	names := make([]string, 0, len(byPlaybook))
	for n := range byPlaybook {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, n := range names {
		if text[n] == "" {
			continue
		}
		fmt.Fprintf(&sb, "\n--- Playbook %s (%s) ---\n%s", n, strings.Join(byPlaybook[n], ", "), text[n])
	}
	return sb.String(), nil
}

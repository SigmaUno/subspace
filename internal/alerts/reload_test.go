package alerts_test

import (
	"context"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/alerts"
	"github.com/SigmaUno/subspace/internal/model"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestReloadRejectsInvalidThresholdsAndKeepsCurrent(t *testing.T) {
	eng := alerts.New(alerts.Options{Source: &script{}, Thresholds: alerts.Defaults()})
	before := eng.Thresholds()
	for _, bad := range []map[string]string{
		{alerts.EnvStallAfter: "soon"},
		{alerts.EnvPausedMax: "-1"},
		{alerts.EnvLaggedRatio: "1.5"},
		{alerts.EnvDiskMinFree: "lots", alerts.EnvReviewAfter: "-5m"},
	} {
		if _, err := eng.PrepareReload(env(bad)); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	if eng.Thresholds() != before {
		t.Fatal("thresholds changed by rejected reloads")
	}
}

func TestReloadAppliesThresholdsToNextEvaluationAndKeepsInterval(t *testing.T) {
	now := t0
	src := &script{}
	eng := alerts.New(alerts.Options{Source: src, Thresholds: alerts.Defaults(), Now: func() time.Time { return now }})
	src.set(func(s *alerts.Snapshot) {
		s.Tasks = []*model.Task{{ID: "T-1", Status: model.StatusReady, UpdatedAt: t0.Add(-5 * time.Minute)}}
	})
	if err := eng.Evaluate(context.Background()); err != nil || len(eng.List("", 0)) != 0 {
		t.Fatalf("raised under default threshold: %v", err)
	}

	plan, err := eng.PrepareReload(env(map[string]string{alerts.EnvStallAfter: "1m", alerts.EnvInterval: "5s"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Ignored) != 1 || plan.Ignored[0].Key != alerts.EnvInterval {
		t.Fatalf("ignored = %+v", plan.Ignored)
	}
	plan.Commit()
	if th := eng.Thresholds(); th.StallAfter != time.Minute || th.Interval != alerts.Defaults().Interval {
		t.Fatalf("thresholds = %+v", th)
	}
	if err := eng.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := eng.List(alerts.StatusActive, 0); len(got) != 1 || got[0].ID != "swarm_stalled" {
		t.Fatalf("active = %+v", got)
	}
}

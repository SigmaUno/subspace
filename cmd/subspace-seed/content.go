package main

import (
	"math"
	"math/rand/v2"
	"slices"
)

var progressNotes = []string{
	"Branch is up, wiring the first tests",
	"Core path works, handling error cases now",
	"Refactored a helper that was duplicated in two places",
	"Edge cases covered, cleaning up naming",
	"Running the full suite before pushing",
	"CI is green on the branch",
}

var workNotes = []string{
	"Started on %q. Reading the surrounding code first.",
	"Reusing an existing helper for %q instead of adding a new one.",
	"A flaky test unrelated to %q failed once and passed on re-run.",
	"Scope check for %q: staying inside the files named in the task.",
	"Pushed a first version of %q, tests still running.",
}

var summaries = []string{
	"Implementation matches the acceptance criteria, tests added.",
	"Handled the empty and oversized input cases explicitly.",
	"Small diff, no new dependencies.",
	"Added table-driven tests for the edge cases.",
}

var approvals = []string{
	"Matches the acceptance criteria. Tests cover the edge cases.",
	"Clean diff, error handling is consistent with the rest of the package.",
	"Verified the branch locally. Approving.",
}

var objections = []string{
	"The error is swallowed in the handler. Return it to the caller and add a regression test",
	"One acceptance criterion has no test. Please add it",
	"Naming is inconsistent with the neighbouring package, and the doc string is stale",
	"This reads the whole body before checking its size. Check the limit first",
}

var briefUpdates = []string{
	"Plan for %q is in place. Workers are picking up the first tasks.",
	"Status on %q: dependencies are being cleared in order, reviews are running in parallel.",
	"Keeping %q within budget. Costs are tracking below the cap so far.",
}

var leaderNudges = []string{
	"Priority change: finish the open review comments before claiming anything new.",
	"Reminder: keep the PR small and link it in your task thread.",
	"Heads up, a dependency of your next task is about to merge.",
}

var ownerBriefReplies = []string{
	"Approved. Please keep the PRs small, I review them in the evening.",
	"Good plan. Do not touch the build config.",
	"Looks reasonable. I want to see the quota design before any code.",
}

var ownerTaskReplies = []struct{ Ref, Text string }{
	{"w3", "Looks good. I will merge once the CI run finishes."},
	{"d2", "Nice work, the board feels instant now."},
	{"w14", "Pausing this one, I will look at the fuzz findings myself."},
	{"d9", "Hold on until I decide about the state library."},
}

func pick[T any](r *rand.Rand, xs []T) T { return xs[r.IntN(len(xs))] }

func ptr[T any](v T) *T { return &v }

func contains(xs []string, v string) bool { return slices.Contains(xs, v) }

func round2(f float64) float64 { return math.Round(f*100) / 100 }

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

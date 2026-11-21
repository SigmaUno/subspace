package threads

import (
	"math/rand"
	"testing"

	"github.com/SigmaUno/subspace/internal/model"
)

func TestAgentsNeverAccessForeignThreads(t *testing.T) {
	ids := []string{"", "me", "owner", "rex", "claude-1", "claude-2", "a:b", " rex", "REX"}
	kinds := []string{model.ThreadAgent, model.ThreadTask, model.ThreadBrief, "other", ""}
	roles := []string{model.RoleWorker, model.RoleTester, model.RoleReviewer}

	for seed := int64(1); seed <= 5000; seed++ {
		r := rand.New(rand.NewSource(seed))
		actor := model.Actor{ID: ids[r.Intn(len(ids))], Role: roles[r.Intn(len(roles))]}
		input := kinds[r.Intn(len(kinds))] + ":" + ids[r.Intn(len(ids))]
		assignee, reviewer := ids[r.Intn(len(ids))], ids[r.Intn(len(ids))]

		ref, err := Parse(input, actor.ID)
		if err != nil {
			continue
		}
		if !CanAccess(actor, ref, assignee, reviewer) {
			continue
		}
		switch ref.Kind {
		case model.ThreadAgent:
			if actor.ID == "" || ref.ID != actor.ID {
				t.Fatalf("seed %d: %+v read %q as %+v", seed, actor, input, ref)
			}
		case model.ThreadTask:
			if actor.ID == "" || (assignee != actor.ID && reviewer != actor.ID) {
				t.Fatalf("seed %d: %+v read task thread %q owned by %q/%q", seed, actor, input, assignee, reviewer)
			}
		default:
			t.Fatalf("seed %d: %s %+v read %q", seed, actor.Role, actor, input)
		}
	}
}

func TestParseTopicRoundTrip(t *testing.T) {
	ids := []string{"T-1", "B-2", "claude-1", "a:b", "x y"}
	kinds := []string{model.ThreadAgent, model.ThreadTask, model.ThreadBrief}
	for seed := int64(1); seed <= 1000; seed++ {
		r := rand.New(rand.NewSource(seed))
		kind, id := kinds[r.Intn(len(kinds))], ids[r.Intn(len(ids))]
		ref, err := Parse(kind+":"+id, "self")
		if err != nil || ref != (Ref{Kind: kind, ID: id}) {
			t.Fatalf("seed %d: Parse(%s:%s) = %+v, %v", seed, kind, id, ref, err)
		}
		if got, want := ref.Topic(), "thread:"+kind+":"+id; got != want {
			t.Fatalf("seed %d: topic %q, want %q", seed, got, want)
		}
	}
}

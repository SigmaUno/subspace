package dst

import (
	"fmt"
	"slices"
	"strings"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
)

func (g *Gen) v4op(s *State) Op {
	r := g.v4r
	switch p := r.Intn(100); {
	case p < 22:
		a := g.anyAgent()
		if r.Intn(10) < 7 {
			if as := g.gatewayAgents(); len(as) > 0 {
				a = as[r.Intn(len(as))]
			}
		}
		op := g.actor(s, a)
		tools := append(slices.Clone(upstreamTools), "ghost")
		op.Kind, op.Target = OpGatewayCall, tools[r.Intn(len(tools))]
		return op
	case p < 27:
		op := g.actor(s, g.anyAgent())
		op.Kind = OpGatewayList
		return op
	case p < 50:
		a := g.workerSim()
		op := g.actor(s, a)
		op.Kind = OpLogOutput
		if mine := g.mine(s, a.spec.ID, model.StatusAssigned, model.StatusInProgress, model.StatusChangesRequested); len(mine) > 0 && r.Intn(5) > 0 {
			op.Task = mine[r.Intn(len(mine))].ID
		}
		lines := make([]string, 1+r.Intn(3))
		for i := range lines {
			lines[i] = fmt.Sprintf("out %d of %s", r.Intn(1000), a.spec.ID)
		}
		op.Text = strings.Join(lines, "\n")
		if r.Intn(2) == 0 {
			if r.Intn(6) > 0 || g.seqs[a.spec.ID] == 0 {
				g.seqs[a.spec.ID]++
			}
			op.N = g.seqs[a.spec.ID]
		}
		return op
	case p < 58:
		return Op{Kind: OpTranscriptRead, Task: g.anyTask(s)}
	case p < 68:
		return g.leak(s)
	case p < 78:
		var leaked []string
		for _, a := range s.Agents {
			if a.Status == model.AgentRevoked && strings.HasPrefix(deref(a.PauseReason), "token leaked") {
				leaked = append(leaked, a.ID)
			}
		}
		if len(leaked) > 0 {
			return Op{Kind: OpReissue, Target: leaked[r.Intn(len(leaked))]}
		}
		return Op{Kind: OpDrill}
	case p < 83:
		return Op{Kind: OpDrill}
	case p < 90:
		return Op{Kind: OpPushCut}
	default:
		return Op{Kind: OpPushSettle}
	}
}

func (g *Gen) gatewayAgents() []*agentSim {
	var out []*agentSim
	for _, a := range append([]*agentSim{g.leader}, g.agents...) {
		if slices.Contains(g.w.Server.V4.Roles, a.spec.Role) {
			out = append(out, a)
		}
	}
	return out
}

func (g *Gen) leak(s *State) Op {
	r := g.v4r
	channel := leakChannels[r.Intn(len(leakChannels))]
	var statuses []string
	switch channel {
	case board.ChannelNote, board.ChannelQuestion:
		statuses = []string{model.StatusAssigned, model.StatusInProgress, model.StatusChangesRequested}
	case board.ChannelSummary, board.ChannelStatus, board.ChannelTests:
		statuses = []string{model.StatusInProgress}
	case board.ChannelHelp:
		statuses = []string{model.StatusAssigned, model.StatusInProgress}
	case board.ChannelReview:
		statuses = []string{model.StatusSubmitted, model.StatusInReview}
	}
	a, task := g.anyAgent(), ""
	if statuses != nil {
		ts := g.tasksIn(s, statuses...)
		if channel == board.ChannelTests {
			ts = slices.DeleteFunc(ts, func(t *model.Task) bool { return t.Role != model.RoleTester })
		}
		ts = slices.DeleteFunc(ts, func(t *model.Task) bool { return t.Assignee == nil })
		if len(ts) == 0 {
			channel = board.ChannelMessage
		} else {
			t := ts[r.Intn(len(ts))]
			task = t.ID
			id := *t.Assignee
			if channel == board.ChannelReview {
				id = g.agentID(model.RoleReviewer)
			}
			for _, x := range g.agents {
				if x.spec.ID == id {
					a = x
				}
			}
		}
	}
	op := g.actor(s, a)
	victim := g.anyAgent().spec.ID
	if r.Intn(3) == 0 {
		victim = a.spec.ID
	}
	op.Kind, op.Target, op.Text, op.Task = OpLeak, victim, channel, task
	op.N = s.Tokens[victim]
	if op.N > 0 && r.Intn(5) == 0 {
		op.N--
	}
	return op
}

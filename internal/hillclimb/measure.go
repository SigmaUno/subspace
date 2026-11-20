package hillclimb

import (
	"context"
	"math"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

const maxLog = 4096

type Runner interface {
	Run(ctx context.Context, repo, branch, cmd string) (string, error)
}

type Measurer struct {
	Store  Store
	Runner Runner
	Now    func() time.Time

	mu       sync.Mutex
	inflight map[string]int
}

func NewMeasurer(s Store, r Runner) *Measurer {
	return &Measurer{Store: s, Runner: r, Now: time.Now, inflight: map[string]int{}}
}

func (m *Measurer) CreateJob(j *Job) error {
	if err := j.Validate(); err != nil {
		return err
	}
	j.CreatedAt = m.Now().UTC()
	return m.Store.CreateJob(j)
}

func (m *Measurer) Measure(ctx context.Context, jobID, agentID, branch string) (*Attempt, *Job, error) {
	if branch == "" {
		return nil, nil, model.BadRequest("branch is required")
	}
	j, err := m.reserve(jobID)
	if err != nil {
		return nil, j, err
	}
	out, runErr := m.Runner.Run(ctx, j.Repo, branch, j.MetricCmd)
	a := &Attempt{JobID: jobID, AgentID: agentID, Branch: branch, Status: AttemptOK, Log: tail(out)}
	if runErr != nil {
		a.Status = AttemptFailed
		a.Log = tail(out + "\n" + runErr.Error())
	} else if v, ok := LastNumber(out); ok {
		a.Value = &v
	} else {
		a.Status = AttemptFailed
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.inflight[jobID]--
	a.MeasuredAt = m.Now().UTC()
	if j, err = m.Store.GetJob(jobID); err != nil {
		return nil, nil, err
	}
	if err := m.Store.AddAttempt(a); err != nil {
		return nil, nil, err
	}
	j.record(a)
	if err := m.Store.UpdateJob(j); err != nil {
		return a, nil, err
	}
	return a, j, nil
}

func (m *Measurer) reserve(jobID string) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inflight == nil {
		m.inflight = map[string]int{}
	}
	j, err := m.Store.GetJob(jobID)
	if err != nil {
		return nil, err
	}
	if j.Status == JobStopped {
		return j, model.Errf("conflict", "hillclimb job %s is stopped (%s)", jobID, *j.StopReason)
	}
	if j.Budget > 0 && j.Attempts+m.inflight[jobID] >= j.Budget {
		return j, model.Errf("conflict", "hillclimb job %s has no budget left", jobID)
	}
	m.inflight[jobID]++
	return j, nil
}

type Point struct {
	N          int       `json:"n"`
	AttemptID  int64     `json:"attempt_id"`
	AgentID    string    `json:"agent_id"`
	Branch     string    `json:"branch"`
	Value      *float64  `json:"value"`
	Best       *float64  `json:"best"`
	MeasuredAt time.Time `json:"measured_at"`
}

func (m *Measurer) Series(jobID string) (*Job, []Point, error) {
	j, err := m.Store.GetJob(jobID)
	if err != nil {
		return nil, nil, err
	}
	as, err := m.Store.ListAttempts(jobID)
	if err != nil {
		return nil, nil, err
	}
	pts := make([]Point, 0, len(as))
	var best *float64
	for i, a := range as {
		if a.Value != nil && (best == nil || j.better(*a.Value, *best)) {
			best = a.Value
		}
		pts = append(pts, Point{
			N: i + 1, AttemptID: a.ID, AgentID: a.AgentID, Branch: a.Branch,
			Value: a.Value, Best: best, MeasuredAt: a.MeasuredAt,
		})
	}
	return j, pts, nil
}

var numRe = regexp.MustCompile(`[-+]?(?:\d+\.?\d*|\.\d+)(?:[eE][-+]?\d+)?`)

func LastNumber(s string) (float64, bool) {
	ms := numRe.FindAllString(s, -1)
	for i := len(ms) - 1; i >= 0; i-- {
		v, err := strconv.ParseFloat(ms[i], 64)
		if err == nil && !math.IsInf(v, 0) && !math.IsNaN(v) {
			return v, true
		}
	}
	return 0, false
}

func tail(s string) string {
	if len(s) <= maxLog {
		return s
	}
	return s[len(s)-maxLog:]
}

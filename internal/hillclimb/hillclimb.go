package hillclimb

import (
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	DirectionMin = "min"
	DirectionMax = "max"

	JobRunning = "running"
	JobStopped = "stopped"

	AttemptOK     = "ok"
	AttemptFailed = "failed"

	StopTarget  = "target_reached"
	StopBudget  = "budget_exhausted"
	StopPlateau = "plateau"
)

type Job struct {
	ID            string    `json:"id"`
	BriefID       string    `json:"brief_id"`
	Repo          string    `json:"repo"`
	MetricCmd     string    `json:"metric_cmd"`
	Direction     string    `json:"direction"`
	Target        *float64  `json:"target"`
	Budget        int       `json:"budget"`
	PlateauN      int       `json:"plateau_n"`
	Status        string    `json:"status"`
	StopReason    *string   `json:"stop_reason"`
	Best          *float64  `json:"best"`
	BestAttemptID *int64    `json:"best_attempt_id"`
	Attempts      int       `json:"attempts"`
	SinceBest     int       `json:"since_best"`
	CreatedAt     time.Time `json:"created_at"`
}

type Attempt struct {
	ID         int64     `json:"id"`
	JobID      string    `json:"job_id"`
	AgentID    string    `json:"agent_id"`
	Branch     string    `json:"branch"`
	Value      *float64  `json:"value"`
	Status     string    `json:"status"`
	MeasuredAt time.Time `json:"measured_at"`
	Log        string    `json:"log"`
}

func (j *Job) Validate() error {
	switch {
	case j.BriefID == "":
		return model.BadRequest("brief_id is required")
	case j.Repo == "":
		return model.BadRequest("repo is required")
	case j.MetricCmd == "":
		return model.BadRequest("metric_cmd is required")
	case j.Direction != DirectionMin && j.Direction != DirectionMax:
		return model.BadRequest("direction must be min or max")
	case j.Budget < 0:
		return model.BadRequest("budget must be >= 0")
	case j.PlateauN < 0:
		return model.BadRequest("plateau_n must be >= 0")
	}
	return nil
}

func (j *Job) better(v, than float64) bool {
	if j.Direction == DirectionMax {
		return v > than
	}
	return v < than
}

func (j *Job) reached(v float64) bool {
	if j.Target == nil {
		return false
	}
	if j.Direction == DirectionMax {
		return v >= *j.Target
	}
	return v <= *j.Target
}

func (j *Job) record(a *Attempt) {
	j.Attempts++
	if a.Value != nil && (j.Best == nil || j.better(*a.Value, *j.Best)) {
		v, id := *a.Value, a.ID
		j.Best, j.BestAttemptID, j.SinceBest = &v, &id, 0
	} else {
		j.SinceBest++
	}
	if j.Status == JobStopped {
		return
	}
	var reason string
	switch {
	case j.Best != nil && j.reached(*j.Best):
		reason = StopTarget
	case j.Budget > 0 && j.Attempts >= j.Budget:
		reason = StopBudget
	case j.PlateauN > 0 && j.SinceBest >= j.PlateauN:
		reason = StopPlateau
	default:
		return
	}
	j.Status, j.StopReason = JobStopped, &reason
}

type Store interface {
	CreateJob(j *Job) error
	GetJob(id string) (*Job, error)
	ListJobs() ([]Job, error)
	UpdateJob(j *Job) error
	AddAttempt(a *Attempt) error
	ListAttempts(jobID string) ([]Attempt, error)
}

type MemStore struct {
	mu       sync.Mutex
	jobs     map[string]*Job
	order    []string
	attempts map[string][]Attempt
	nextJob  int
	nextAtt  int64
}

func NewMemStore() *MemStore {
	return &MemStore{jobs: map[string]*Job{}, attempts: map[string][]Attempt{}}
}

func (s *MemStore) CreateJob(j *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextJob++
	j.ID = fmt.Sprintf("H-%d", s.nextJob)
	j.Status = JobRunning
	c := *j
	s.jobs[j.ID] = &c
	s.order = append(s.order, j.ID)
	return nil
}

func (s *MemStore) GetJob(id string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return nil, model.NotFound("hillclimb job %s not found", id)
	}
	c := *j
	return &c, nil
}

func (s *MemStore) ListJobs() ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Job, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, *s.jobs[id])
	}
	return out, nil
}

func (s *MemStore) UpdateJob(j *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[j.ID]; !ok {
		return model.NotFound("hillclimb job %s not found", j.ID)
	}
	c := *j
	s.jobs[j.ID] = &c
	return nil
}

func (s *MemStore) AddAttempt(a *Attempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[a.JobID]; !ok {
		return model.NotFound("hillclimb job %s not found", a.JobID)
	}
	s.nextAtt++
	a.ID = s.nextAtt
	s.attempts[a.JobID] = append(s.attempts[a.JobID], *a)
	return nil
}

func (s *MemStore) ListAttempts(jobID string) ([]Attempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[jobID]; !ok {
		return nil, model.NotFound("hillclimb job %s not found", jobID)
	}
	return slices.Clone(s.attempts[jobID]), nil
}

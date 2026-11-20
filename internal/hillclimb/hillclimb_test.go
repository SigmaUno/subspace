package hillclimb_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/hillclimb"
)

type fakeRunner struct {
	mu    sync.Mutex
	outs  []string
	errAt map[int]bool
	calls int
	seen  []string
}

func (f *fakeRunner) Run(_ context.Context, repo, branch, cmd string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.calls
	f.calls++
	f.seen = append(f.seen, repo+"|"+branch+"|"+cmd)
	if f.errAt[i] {
		return "Traceback: boom", errors.New("exit status 1")
	}
	return f.outs[i%len(f.outs)], nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time {
	c.t = c.t.Add(time.Minute)
	return c.t
}

func newEnv(r hillclimb.Runner) (*hillclimb.Measurer, *httptest.Server) {
	m := hillclimb.NewMeasurer(hillclimb.NewMemStore(), r)
	c := &clock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	m.Now = c.now
	return m, httptest.NewServer(hillclimb.NewHandler(m))
}

func postJob(t *testing.T, srv *httptest.Server, body string) (*http.Response, hillclimb.Job) {
	t.Helper()
	resp, err := srv.Client().Post(srv.URL+"/hillclimb/jobs", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var j hillclimb.Job
	_ = json.NewDecoder(resp.Body).Decode(&j)
	return resp, j
}

func getJSON(t *testing.T, srv *httptest.Server, path string, v any) int {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

func TestDecreasingLossReachesTarget(t *testing.T) {
	losses := []float64{2.0, 1.4, 1.5, 0.9, 0.6, 0.61, 0.3, 0.09}
	outs := make([]string, len(losses))
	for i, l := range losses {
		outs[i] = fmt.Sprintf("epoch %d/10 lr=3e-4\nval loss: %.2f\n", i+1, l)
	}
	r := &fakeRunner{outs: outs, errAt: map[int]bool{2: true}}
	m, srv := newEnv(r)
	defer srv.Close()

	resp, job := postJob(t, srv, `{"brief_id":"B-1","repo":"SigmaUno/nn","metric_cmd":"python eval.py","direction":"min","target":0.1,"budget":20,"plateau_n":4}`)
	if resp.StatusCode != http.StatusCreated || job.ID == "" || job.Status != hillclimb.JobRunning {
		t.Fatalf("create: %d %+v", resp.StatusCode, job)
	}

	var final *hillclimb.Job
	for i := 0; ; i++ {
		_, j, err := m.Measure(context.Background(), job.ID, "opencode-1", fmt.Sprintf("sub/h-%d", i))
		if err != nil {
			t.Fatalf("measure %d: %v", i, err)
		}
		if j.Status == hillclimb.JobStopped {
			final = j
			break
		}
	}
	if *final.StopReason != hillclimb.StopTarget || *final.Best != 0.09 || final.Attempts != 8 {
		t.Fatalf("final job: %+v", final)
	}
	if _, _, err := m.Measure(context.Background(), job.ID, "opencode-1", "sub/h-late"); err == nil {
		t.Fatal("measure on stopped job succeeded")
	}
	if r.seen[0] != "SigmaUno/nn|sub/h-0|python eval.py" {
		t.Fatalf("runner got %q", r.seen[0])
	}

	var series struct {
		Job    hillclimb.Job     `json:"job"`
		Points []hillclimb.Point `json:"points"`
	}
	if code := getJSON(t, srv, "/hillclimb/jobs/"+job.ID+"/series", &series); code != http.StatusOK {
		t.Fatalf("series status %d", code)
	}
	if len(series.Points) != 8 {
		t.Fatalf("points: %d", len(series.Points))
	}
	wantBest := []float64{2.0, 1.4, 1.4, 0.9, 0.6, 0.6, 0.3, 0.09}
	for i, p := range series.Points {
		if p.N != i+1 || p.Best == nil || *p.Best != wantBest[i] {
			t.Fatalf("point %d: %+v best=%v", i, p, p.Best)
		}
	}
	if series.Points[2].Value != nil {
		t.Fatalf("failed attempt has value %v", *series.Points[2].Value)
	}
	if !series.Points[1].MeasuredAt.After(series.Points[0].MeasuredAt) {
		t.Fatal("measured_at not increasing")
	}

	var list struct {
		Jobs []hillclimb.Job `json:"jobs"`
	}
	getJSON(t, srv, "/hillclimb/jobs", &list)
	if len(list.Jobs) != 1 || list.Jobs[0].Status != hillclimb.JobStopped || *list.Jobs[0].BestAttemptID != series.Points[7].AttemptID {
		t.Fatalf("list: %+v", list.Jobs)
	}
}

func TestStopConditions(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		outs     []string
		reason   string
		attempts int
		best     float64
	}{
		{"plateau", `"direction":"min","plateau_n":3`, []string{"1.0", "0.8", "0.9", "0.85", "0.8"}, hillclimb.StopPlateau, 5, 0.8},
		{"budget", `"direction":"min","budget":4`, []string{"5", "4", "3", "2", "1"}, hillclimb.StopBudget, 4, 2},
		{"max target", `"direction":"max","target":0.9,"plateau_n":10`, []string{"acc 0.5", "acc 0.7", "acc 0.95"}, hillclimb.StopTarget, 3, 0.95},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, srv := newEnv(&fakeRunner{outs: tc.outs})
			defer srv.Close()
			_, job := postJob(t, srv, `{"brief_id":"B-1","repo":"r","metric_cmd":"make bench",`+tc.body+`}`)
			var j *hillclimb.Job
			for range 50 {
				var err error
				if _, j, err = m.Measure(context.Background(), job.ID, "a", "b"); err != nil {
					t.Fatal(err)
				}
				if j.Status == hillclimb.JobStopped {
					break
				}
			}
			if j.Status != hillclimb.JobStopped || *j.StopReason != tc.reason || j.Attempts != tc.attempts || *j.Best != tc.best {
				t.Fatalf("got status=%s reason=%v attempts=%d best=%v", j.Status, j.StopReason, j.Attempts, *j.Best)
			}
		})
	}
}

func TestConcurrentMeasureRespectsBudget(t *testing.T) {
	m, srv := newEnv(&fakeRunner{outs: []string{"3", "2", "1"}})
	defer srv.Close()
	_, job := postJob(t, srv, `{"brief_id":"B-1","repo":"r","metric_cmd":"x","direction":"min","budget":7}`)
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Go(func() { _, _, _ = m.Measure(context.Background(), job.ID, fmt.Sprintf("w-%d", i), "b") })
	}
	wg.Wait()
	_, pts, err := m.Series(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 7 {
		t.Fatalf("attempts = %d, want 7", len(pts))
	}
}

func TestCreateValidationAndNotFound(t *testing.T) {
	_, srv := newEnv(&fakeRunner{outs: []string{"1"}})
	defer srv.Close()
	for _, body := range []string{
		`{"brief_id":"B-1","repo":"r","metric_cmd":"x","direction":"up"}`,
		`{"brief_id":"B-1","repo":"r","direction":"min"}`,
		`{not json`,
	} {
		if resp, _ := postJob(t, srv, body); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: status %d", body, resp.StatusCode)
		}
	}
	var e struct {
		Error struct{ Code string } `json:"error"`
	}
	if code := getJSON(t, srv, "/hillclimb/jobs/H-99/series", &e); code != http.StatusNotFound || e.Error.Code != "not_found" {
		t.Fatalf("series of missing job: %d %+v", code, e)
	}
}

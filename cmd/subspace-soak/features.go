package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // fake commit ids, not security
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	mrand "math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/agentout"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	seedAge     = 40 * 24 * time.Hour
	retainDays  = "30"
	streamBatch = 50
	invRetain   = "retention_verified"
	invDrill    = "drill_passed"
)

type fakes struct {
	forge *httptest.Server
	sink  *httptest.Server
	mu    sync.Mutex
	hits  map[string]int64
	rnd   *mrand.Rand
}

func newFakes(seed uint64) *fakes {
	f := &fakes{hits: map[string]int64{}, rnd: mrand.New(mrand.NewPCG(seed, 99))} //nolint:gosec // reproducible faults
	f.forge = httptest.NewServer(http.HandlerFunc(f.serveForge))
	f.sink = httptest.NewServer(http.HandlerFunc(f.serveSink))
	return f
}

func (f *fakes) close() {
	f.forge.Close()
	f.sink.Close()
}

func (f *fakes) hit(k string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits[k]++
	return f.rnd.IntN(100)
}

func (f *fakes) counts() map[string]int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]int64, len(f.hits))
	for k, v := range f.hits {
		out[k] = v
	}
	return out
}

func fakeSHA(s string) string {
	sum := sha1.Sum([]byte(s)) //nolint:gosec // fake commit ids
	return hex.EncodeToString(sum[:])
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakes) serveForge(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/v3/repos/"+soakRepo+"/")
	if !ok {
		f.hit("forge_unknown")
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(rest, "/")
	if roll := f.hit("forge_" + parts[0]); roll < 3 {
		f.hit("forge_fault")
		http.Error(w, "fake outage", http.StatusBadGateway)
		return
	}
	switch {
	case r.Method == http.MethodPost && parts[0] == "statuses":
		writeJSON(w, http.StatusCreated, map[string]any{"state": "success"})
	case parts[0] == "pulls" && len(parts) == 2:
		writeJSON(w, http.StatusOK, map[string]any{"state": "open", "title": "soak " + parts[1], "merged": false, "merged_at": nil,
			"draft": false, "mergeable": true, "head": map[string]any{"sha": fakeSHA("pr" + parts[1]), "ref": "sub/soak-" + parts[1]}})
	case parts[0] == "pulls" && len(parts) == 3 && parts[2] == "reviews":
		writeJSON(w, http.StatusOK, []any{map[string]any{"user": map[string]any{"login": "rev"}, "state": "APPROVED", "dismissed": false}})
	case parts[0] == "pulls" && len(parts) == 3 && parts[2] == "files":
		writeJSON(w, http.StatusOK, []any{map[string]any{"filename": "internal/soak/a.go", "previous_filename": "", "additions": 3, "deletions": 1}})
	case parts[0] == "pulls" && len(parts) == 3 && parts[2] == "commits":
		writeJSON(w, http.StatusOK, []any{map[string]any{"sha": fakeSHA("pr" + parts[1]), "author": map[string]any{"login": "worker"},
			"commit": map[string]any{"author": map[string]any{"email": "worker@soak.invalid"}}}})
	case parts[0] == "commits" && len(parts) == 3 && parts[2] == "status":
		writeJSON(w, http.StatusOK, map[string]any{"state": "success", "total_count": 1})
	case parts[0] == "commits" && len(parts) == 3 && parts[2] == "check-runs":
		writeJSON(w, http.StatusOK, map[string]any{"check_runs": []any{map[string]any{"status": "completed", "conclusion": "success"}}})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakes) serveSink(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	key := strings.Trim(strings.ReplaceAll(r.URL.Path, "/", "_"), "_")
	roll := f.hit(key)
	switch {
	case strings.HasSuffix(key, "flaky") && roll < 40:
		f.hit(key + "_fault")
		http.Error(w, "flaky", http.StatusInternalServerError)
		return
	case strings.HasSuffix(key, "slow"):
		time.Sleep(time.Duration(100+roll*4) * time.Millisecond)
	}
	w.WriteHeader(http.StatusNoContent)
}

func featureEnv(dir string, f *fakes) (map[string]string, error) {
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o750); err != nil {
		return nil, err
	}
	git := "#!/bin/sh\ncase \"$1\" in rev-parse) printf '%s' \"$*\" | sha1sum | cut -c1-40 ;; esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(git), 0o700); err != nil { //nolint:gosec // executable fake git
		return nil, err
	}
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	s := f.sink.URL
	return map[string]string{
		"PATH":                              bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"SUBSPACE_AUTOPILOT":                "on",
		"SUBSPACE_AUTOPILOT_INTERVAL":       "3s",
		"SUBSPACE_NOTIFY_ALLOW_PRIVATE":     "1",
		"SUBSPACE_NOTIFY_WEBHOOK_URL":       s + "/notify/webhook",
		"SUBSPACE_NOTIFY_NTFY_URL":          s + "/notify/ntfy/soak",
		"SUBSPACE_NOTIFY_SLACK_URL":         s + "/notify/slack",
		"SUBSPACE_NOTIFY_SECRET":            hex.EncodeToString(key),
		"SUBSPACE_WEBHOOKS_OUT_ALLOW_CIDRS": "127.0.0.0/8",
		"SUBSPACE_RETAIN_INTERVAL":          "3m",
		"SUBSPACE_RETAIN_AUDIT_DAYS":        retainDays,
		"SUBSPACE_RETAIN_MESSAGES_DAYS":     retainDays,
		"SUBSPACE_RETAIN_USAGE_DAYS":        retainDays,
		"SUBSPACE_RETAIN_IDEMPOTENCY_DAYS":  retainDays,
		"SUBSPACE_RETAIN_SEGMENT_ROWS":      "200",
		"SUBSPACE_RETAIN_BATCH":             "100",
		"SUBSPACE_DRILL_EVERY":              "4m",
		"SUBSPACE_SCHEDULE_INTERVAL":        "5s",
		"SUBSPACE_PRSYNC_INTERVAL":          "10s",
		"SUBSPACE_MQ_INTERVAL":              "5s",
	}, nil
}

func applyEnv(env map[string]string) error {
	for k, v := range env {
		if _, ok := os.LookupEnv(k); ok && k != "PATH" {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	return nil
}

func (h *harness) prURL(t *model.Task) string {
	if h.fakes == nil {
		return "https://git.example/soak/pull/" + strings.TrimPrefix(t.ID, "T-")
	}
	return h.fakes.forge.URL + "/" + soakRepo + "/pull/" + strings.TrimPrefix(t.ID, "T-")
}

func (h *harness) post(ctx context.Context, op, path, idem string, in, out any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.proc.url()+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.proc.cfg.OwnerToken)
	req.Header.Set("Content-Type", "application/json")
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	start := time.Now()
	resp, err := h.hc.Do(req)
	if err != nil {
		h.stats.observe("rest:"+op, time.Since(start), &client.TransportError{Err: err})
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		err = &client.Error{Status: resp.StatusCode, Code: apiCode(body), Message: string(body)}
		h.stats.observe("rest:"+op, time.Since(start), err)
		return err
	}
	h.stats.observe("rest:"+op, time.Since(start), nil)
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

func apiCode(body []byte) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	return e.Error.Code
}

func (h *harness) seed(ctx context.Context) error {
	repo := map[string]any{"name": soakRepo, "host": "github", "base_url": h.fakes.forge.URL, "default_branch": "main",
		"merge_test_command": []string{"true"}, "enabled": true}
	if err := h.post(ctx, "seed_repo", "/api/v1/repos", "", repo, nil); err != nil {
		return fmt.Errorf("register repo: %w", err)
	}
	if _, err := h.register(ctx, model.RoleWorker, 4); err != nil {
		return err
	}
	for i := range 3 {
		var b model.Brief
		in := map[string]any{"title": fmt.Sprintf("Seed brief %d", i), "body": "Old history.", "repos": []string{soakRepo}}
		if err := h.post(ctx, "seed_brief", "/api/v1/briefs", fmt.Sprintf("seed-%d-%d", h.cfg.Seed, i), in, &b); err != nil {
			return fmt.Errorf("seed brief: %w", err)
		}
		for j := range 250 {
			if _, err := h.owner.PostThreadMessage(ctx, model.ThreadBrief, b.ID, fmt.Sprintf("old note %d", j)); err != nil {
				return fmt.Errorf("seed message: %w", err)
			}
		}
		if err := h.ageMessages(ctx, b.ID); err != nil {
			return fmt.Errorf("age seed messages: %w", err)
		}
		closed := "closed"
		if err := h.owner.Do(ctx, "seed_close", http.MethodPatch, "/api/v1/briefs/"+b.ID, map[string]any{"status": closed}, nil); err != nil {
			return fmt.Errorf("seed close: %w", err)
		}
	}
	return nil
}

func (h *harness) ageMessages(ctx context.Context, briefID string) error {
	return h.proc.srv.Store.Atomic(ctx, func(t store.Tx) error {
		q, ok := t.(sqlExec)
		if !ok {
			return nil
		}
		_, err := q.Exec(`UPDATE messages SET created_at = created_at - ? WHERE thread_kind = ? AND thread_id = ?`,
			seedAge.Milliseconds(), model.ThreadBrief, briefID)
		return err
	})
}

type sqlExec interface {
	Exec(q string, args ...any) (sql.Result, error)
}

type streamer struct {
	a    *agent
	m    *client.MCP
	seq  int64
	life time.Duration
}

func (h *harness) takeStreamer(ctx context.Context) (*agent, error) {
	h.streamMu.Lock()
	defer h.streamMu.Unlock()
	h.roster.mu.Lock()
	if p := h.roster.pool["streamer"]; len(p) > 0 {
		a := p[len(p)-1]
		h.roster.pool["streamer"] = p[:len(p)-1]
		h.roster.mu.Unlock()
		return a, nil
	}
	h.roster.mu.Unlock()
	as, err := h.register(ctx, model.RoleLeader, streamBatch)
	if err != nil {
		return nil, err
	}
	h.roster.mu.Lock()
	defer h.roster.mu.Unlock()
	h.roster.pool["streamer"] = append(h.roster.pool["streamer"], as[1:]...)
	return as[0], nil
}

func (h *harness) streamSlot(ctx context.Context, r *mrand.Rand) {
	for ctx.Err() == nil {
		a, err := h.takeStreamer(ctx)
		if err != nil {
			pause(ctx, r, time.Second, 3*time.Second)
			continue
		}
		life := h.cfg.StreamerLife/2 + time.Duration(r.Int64N(int64(h.cfg.StreamerLife)+1))
		h.stream(ctx, &streamer{a: a, m: h.mcp(a.token), life: life}, r)
	}
}

func (h *harness) stream(ctx context.Context, s *streamer, r *mrand.Rand) {
	sctx, cancel := context.WithTimeout(ctx, s.life)
	defer cancel()
	if err := s.m.Initialize(sctx); err != nil {
		pause(ctx, r, time.Second, 2*time.Second)
	}
	h.count("streamer_join")
	streams := []string{"stdout", "stdout", "stderr", "note"}
	for sctx.Err() == nil {
		pause(sctx, r, time.Second, 3*time.Second)
		if sctx.Err() != nil {
			break
		}
		var b strings.Builder
		for i := range 1 + r.IntN(12) {
			fmt.Fprintf(&b, "%s step %d line %d: %s\n", s.a.id, s.seq, i, strings.Repeat("x", r.IntN(160)))
		}
		s.seq++
		seq := s.seq
		if r.IntN(20) == 0 {
			seq--
		}
		if _, err := s.m.LogOutput(sctx, agentout.Input{Stream: streams[r.IntN(len(streams))], Text: b.String(), Seq: &seq}); err == nil {
			h.count("log_output")
		}
	}
	if ctx.Err() != nil {
		return
	}
	lctx, lcancel := context.WithTimeout(ctx, 10*time.Second)
	defer lcancel()
	_ = s.m.Close(lctx)
	if r.IntN(10) < 3 {
		h.roster.mu.Lock()
		h.roster.pool["streamer"] = append(h.roster.pool["streamer"], s.a)
		h.roster.mu.Unlock()
		return
	}
	if _, err := h.owner.RevokeAgent(lctx, s.a.id, "stream ended"); err == nil {
		h.count("streamer_revoke")
	}
}

type featureState struct {
	hooks    map[string]string
	announce []string
	enabled  map[string]bool
}

var hookPaths = []string{"/hook/ok", "/hook/flaky", "/hook/slow"}

var hookEvents = [][]string{{"task.*"}, {"agent.*", "decision.*"}, {"plan.*", "task.updated", "brief.*"}}

func (h *harness) featureSetup(ctx context.Context, fs *featureState) {
	brief := "---\ntitle: Scheduled soak brief\nrepos: [" + soakRepo + "]\nmax_cost_eur: 40\nmax_parallel_agents: 4\n---\nScheduled work.\n"
	in := map[string]any{"name": "soak brief every minute", "cron": "* * * * *", "action": "create_brief",
		"brief": map[string]any{"markdown": brief}, "max_active": 2, "catch_up": "run-once"}
	if err := h.post(ctx, "create_schedule", "/api/v1/schedules", "", in, nil); err == nil {
		h.count("create_schedule")
	}
	for i := range hookPaths {
		h.ensureHook(ctx, fs, i)
	}
}

func (h *harness) ensureHook(ctx context.Context, fs *featureState, i int) {
	if fs.hooks[hookPaths[i]] != "" {
		return
	}
	var out struct {
		Webhook struct {
			ID string `json:"id"`
		} `json:"webhook"`
	}
	in := map[string]any{"url": h.fakes.sink.URL + hookPaths[i], "events": hookEvents[i]}
	if err := h.post(ctx, "create_webhook_out", "/api/v1/webhooks-out", "", in, &out); err == nil {
		fs.hooks[hookPaths[i]] = out.Webhook.ID
		h.count("create_webhook_out")
	}
}

func (h *harness) featureLoop(ctx context.Context, r *mrand.Rand) {
	fs := &featureState{hooks: map[string]string{}, enabled: map[string]bool{}}
	h.featureSetup(ctx, fs)
	for ctx.Err() == nil {
		pause(ctx, r, time.Second, 3*time.Second)
		if ctx.Err() != nil {
			return
		}
		h.featureStep(ctx, fs, r)
	}
}

func (h *harness) featureStep(ctx context.Context, fs *featureState, r *mrand.Rand) {
	switch r.IntN(12) {
	case 0, 1:
		h.announceChurn(ctx, fs, r)
	case 2:
		h.autopilotFlags(ctx, fs, r)
	case 3:
		i := r.IntN(len(hookPaths))
		if id := fs.hooks[hookPaths[i]]; id != "" && r.IntN(5) == 0 {
			if err := h.owner.Do(ctx, "delete_webhook_out", http.MethodDelete, "/api/v1/webhooks-out/"+id, nil, nil); err == nil {
				delete(fs.hooks, hookPaths[i])
				h.count("delete_webhook_out")
			}
		}
		h.ensureHook(ctx, fs, i)
		if id := fs.hooks[hookPaths[i]]; id != "" {
			_ = h.owner.Do(ctx, "webhook_deliveries", http.MethodGet, "/api/v1/webhooks-out/"+id+"/deliveries", nil, nil)
		}
	case 4:
		_ = h.owner.Do(ctx, "agent_output_latest", http.MethodGet, "/api/v1/agent-output", nil, nil)
		if a := h.anyStreamer(); a != "" {
			_ = h.owner.Do(ctx, "agent_output", http.MethodGet, "/api/v1/agents/"+a+"/output?since=0&limit=200", nil, nil)
		}
	case 5:
		_ = h.owner.Do(ctx, "list_prs", http.MethodGet, "/api/v1/prs", nil, nil)
		_ = h.owner.Do(ctx, "mq_list", http.MethodGet, "/api/v1/mergequeue/candidates", nil, nil)
	case 6:
		h.retentionCheck(ctx, r)
	case 7:
		h.drillCheck(ctx, r)
	case 8:
		_ = h.owner.Do(ctx, "list_schedules", http.MethodGet, "/api/v1/schedules", nil, nil)
		_ = h.owner.Do(ctx, "autopilot_status", http.MethodGet, "/api/v1/autopilot", nil, nil)
	default:
		_ = h.owner.Do(ctx, "list_announcements", http.MethodGet, "/api/v1/announcements", nil, nil)
	}
}

func (h *harness) anyStreamer() string {
	h.roster.mu.Lock()
	defer h.roster.mu.Unlock()
	if p := h.roster.pool["streamer"]; len(p) > 0 {
		return p[0].id
	}
	return ""
}

func (h *harness) announceChurn(ctx context.Context, fs *featureState, r *mrand.Rand) {
	if len(fs.announce) > 4 || (len(fs.announce) > 0 && r.IntN(3) == 0) {
		id := fs.announce[0]
		if err := h.owner.Do(ctx, "delete_announcement", http.MethodDelete, "/api/v1/announcements/"+id, nil, nil); err == nil || client.CodeOf(err) == client.CodeNotFound {
			fs.announce = fs.announce[1:]
			h.count("delete_announcement")
		}
		return
	}
	in := map[string]any{"text": fmt.Sprintf("Soak announcement %d: keep branches small.", h.seq.Add(1))}
	if r.IntN(2) == 0 {
		in["expires_at"] = time.Now().Add(time.Duration(10+r.IntN(120)) * time.Second).UTC()
	}
	if bs, err := h.owner.ListBriefs(ctx, "open"); err == nil && len(bs) > 0 && r.IntN(2) == 0 {
		in["brief_id"] = bs[r.IntN(len(bs))].ID
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := h.post(ctx, "create_announcement", "/api/v1/announcements", fmt.Sprintf("an-%d-%d", h.cfg.Seed, h.seq.Add(1)), in, &out); err == nil {
		fs.announce = append(fs.announce, out.ID)
		h.count("create_announcement")
	}
}

func (h *harness) autopilotFlags(ctx context.Context, fs *featureState, r *mrand.Rand) {
	bs, err := h.owner.ListBriefs(ctx, "open")
	if err != nil || len(bs) == 0 {
		return
	}
	b := bs[r.IntN(len(bs))]
	on := r.IntN(3) != 0
	if err := h.post(ctx, "set_autopilot", "/api/v1/autopilot/briefs/"+b.ID, "", map[string]any{"enabled": on}, nil); err == nil {
		h.count("set_autopilot")
	}
}

func (h *harness) retentionCheck(ctx context.Context, r *mrand.Rand) {
	if r.IntN(4) == 0 {
		if err := h.post(ctx, "retention_run", "/api/v1/retention/run", "", map[string]any{}, nil); err == nil {
			h.count("retention_run")
		}
	}
	var v struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := h.owner.Do(ctx, "retention_verify", http.MethodGet, "/api/v1/retention/verify", nil, &v); err != nil {
		return
	}
	h.count("retention_verify")
	if !v.OK {
		h.violate(invRetain, "retention verify failed: "+v.Error)
	}
}

func (h *harness) drillCheck(ctx context.Context, r *mrand.Rand) {
	if h.proc.postgres() {
		return
	}
	if r.IntN(3) == 0 {
		if err := h.post(ctx, "run_drill", "/api/v1/drills", "", map[string]any{}, nil); err == nil {
			h.count("run_drill")
		}
	}
	var out struct {
		Drills []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"drills"`
	}
	if err := h.owner.Do(ctx, "list_drills", http.MethodGet, "/api/v1/drills", nil, &out); err != nil {
		return
	}
	for _, d := range out.Drills {
		if d.Status == "failed" && !strings.Contains(d.Error, "no backup available") && !h.drillSeen(d.ID) {
			h.violate(invDrill, "drill "+d.ID+" failed: "+d.Error)
		}
	}
}

func (h *harness) drillSeen(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.drills == nil {
		h.drills = map[string]bool{}
	}
	seen := h.drills[id]
	h.drills[id] = true
	return seen
}

func (h *harness) enqueueMerge(ctx context.Context, taskID string) bool {
	if h.fakes == nil {
		return false
	}
	if err := h.post(ctx, "mq_enqueue", "/api/v1/mergequeue/candidates", "", map[string]any{"task_id": taskID}, nil); err != nil {
		return false
	}
	h.count("mq_enqueue")
	return true
}

type sqlTx interface {
	Query(q string, args ...any) (*sql.Rows, error)
}

func tableCounts(ctx context.Context, st store.Store, pg bool) map[string]int64 {
	out := map[string]int64{}
	_ = st.View(ctx, func(t store.Tx) error {
		q, ok := t.(sqlTx)
		if !ok {
			return nil
		}
		list := `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`
		if pg {
			list = `SELECT tablename FROM pg_tables WHERE schemaname = current_schema()`
		}
		rows, err := q.Query(list)
		if err != nil {
			return err
		}
		var names []string
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				names = append(names, n)
			}
		}
		_ = rows.Close()
		for _, n := range names {
			rs, err := q.Query(`SELECT COUNT(*) FROM ` + strconv.Quote(n))
			if err != nil {
				continue
			}
			var c int64
			if rs.Next() && rs.Scan(&c) == nil {
				out[n] = c
			}
			_ = rs.Close()
		}
		return nil
	})
	return out
}

func dirBytes(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

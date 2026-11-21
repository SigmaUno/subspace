package alerts

import (
	"fmt"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

func Rules() []Rule {
	return []Rule{
		{"swarm_stalled", swarmStalled},
		{"no_progress", noProgress},
		{"agents_paused", agentsPaused},
		{"agent_flapping", agentFlapping},
		{"review_stuck", reviewStuck},
		{"backup", backupHealth},
		{"offsite", offsiteHealth},
		{"disk_low", diskLow},
		{"db_size", dbSize},
		{"sse_lagging", sseLagging},
	}
}

func Evaluate(s Snapshot, t Thresholds) []Finding {
	var out []Finding
	for _, r := range Rules() {
		out = append(out, r.Eval(s, t)...)
	}
	return out
}

func swarmStalled(s Snapshot, t Thresholds) []Finding {
	if s.Paused || t.StallAfter <= 0 {
		return nil
	}
	var ready int
	var oldest time.Time
	for _, x := range s.Tasks {
		if x.Status != model.StatusReady {
			continue
		}
		ready++
		if oldest.IsZero() || x.UpdatedAt.Before(oldest) {
			oldest = x.UpdatedAt
		}
	}
	if ready == 0 {
		return nil
	}
	var workers, idle int
	for _, a := range s.Agents {
		if a.Status != model.AgentActive || (a.Role != model.RoleWorker && a.Role != model.RoleTester) {
			continue
		}
		workers++
		if a.CurrentTaskID == nil {
			idle++
		}
	}
	if workers > 0 && idle == 0 {
		return nil
	}
	since := oldest
	if s.LastAssignment.After(since) {
		since = s.LastAssignment
	}
	wait := s.Now.Sub(since)
	if wait < t.StallAfter {
		return nil
	}
	return []Finding{{
		Key: "swarm_stalled", Severity: SeverityWarning, Title: "Swarm stalled",
		Summary: fmt.Sprintf("%d ready tasks, %d idle agents of %d, no assignment for %s", ready, idle, workers, round(wait)),
	}}
}

func noProgress(s Snapshot, t Thresholds) []Finding {
	if s.Paused || t.ProgressAfter <= 0 {
		return nil
	}
	var active int
	for _, x := range s.Tasks {
		if x.Status == model.StatusInProgress {
			active++
		}
	}
	if active == 0 {
		return nil
	}
	wait := s.Now.Sub(s.LastTaskChange)
	if wait < t.ProgressAfter {
		return nil
	}
	return []Finding{{
		Key: "no_progress", Severity: SeverityWarning, Title: "No task progress",
		Summary: fmt.Sprintf("%d tasks in progress, no task change for %s", active, round(wait)),
	}}
}

func agentsPaused(s Snapshot, t Thresholds) []Finding {
	if t.PausedMax <= 0 {
		return nil
	}
	var n int
	for _, a := range s.Agents {
		if a.Status == model.AgentPaused {
			n++
		}
	}
	if n < t.PausedMax {
		return nil
	}
	return []Finding{{
		Key: "agents_paused", Severity: SeverityWarning, Title: "Many agents paused",
		Summary: fmt.Sprintf("%d agents are paused (threshold %d)", n, t.PausedMax),
	}}
}

func agentFlapping(s Snapshot, t Thresholds) []Finding {
	if t.FlappingMax <= 0 {
		return nil
	}
	var ids []string
	for _, a := range s.Agents {
		if a.Status == model.AgentPaused && a.PauseReason != nil && strings.HasPrefix(*a.PauseReason, "flapping") {
			ids = append(ids, a.ID)
		}
	}
	if len(ids) < t.FlappingMax {
		return nil
	}
	return []Finding{{
		Key: "agent_flapping", Severity: SeverityWarning, Title: "Agents flapping",
		Summary: fmt.Sprintf("%d agents paused for flapping: %s", len(ids), strings.Join(ids, ", ")),
	}}
}

func reviewStuck(s Snapshot, t Thresholds) []Finding {
	if t.ReviewAfter <= 0 {
		return nil
	}
	var n int
	var oldest *model.Task
	for _, x := range s.Tasks {
		if x.Status != model.StatusSubmitted || x.Reviewer != nil || s.Now.Sub(x.UpdatedAt) < t.ReviewAfter {
			continue
		}
		n++
		if oldest == nil || x.UpdatedAt.Before(oldest.UpdatedAt) {
			oldest = x
		}
	}
	if n == 0 {
		return nil
	}
	return []Finding{{
		Key: "review_stuck", Severity: SeverityWarning, Title: "Review queue stuck",
		TaskID:  oldest.ID,
		Summary: fmt.Sprintf("%d submitted tasks wait for a reviewer, oldest %s for %s", n, oldest.ID, round(s.Now.Sub(oldest.UpdatedAt))),
	}}
}

func backupHealth(s Snapshot, t Thresholds) []Finding {
	if !s.BackupsEnabled {
		return nil
	}
	var out []Finding
	if s.BackupFailing {
		out = append(out, Finding{
			Key: "backup_failing", Severity: SeverityCritical, Title: "Backup failing",
			Summary: "the newest backup does not pass the integrity check",
		})
	}
	if t.BackupMaxAge > 0 && s.Now.Sub(s.LastBackup) >= t.BackupMaxAge {
		out = append(out, Finding{
			Key: "backup_old", Severity: SeverityCritical, Title: "Backup too old",
			Summary: fmt.Sprintf("no backup for %s (limit %s)", round(s.Now.Sub(s.LastBackup)), t.BackupMaxAge),
		})
	}
	return out
}

func offsiteHealth(s Snapshot, t Thresholds) []Finding {
	if !s.OffsiteEnabled || t.OffsiteMaxAge <= 0 || s.Now.Sub(s.LastOffsite) < t.OffsiteMaxAge {
		return nil
	}
	summary := fmt.Sprintf("no verified off-site copy for %s (limit %s)", round(s.Now.Sub(s.LastOffsite)), t.OffsiteMaxAge)
	if s.OffsiteError != "" {
		summary += ": " + s.OffsiteError
	}
	return []Finding{{Key: "offsite_stale", Severity: SeverityCritical, Title: "Off-site backup stale", Summary: summary}}
}

func diskLow(s Snapshot, t Thresholds) []Finding {
	if !s.DiskKnown || t.DiskMinFree == 0 || s.DiskFree >= t.DiskMinFree {
		return nil
	}
	return []Finding{{
		Key: "disk_low", Severity: SeverityCritical, Title: "Data disk almost full",
		Summary: fmt.Sprintf("%s free (limit %s)", bytesString(s.DiskFree), bytesString(t.DiskMinFree)),
	}}
}

func dbSize(s Snapshot, t Thresholds) []Finding {
	var out []Finding
	if t.DBMaxBytes > 0 && s.DBBytes > 0 && uint64(s.DBBytes) > t.DBMaxBytes {
		out = append(out, Finding{
			Key: "db_size", Severity: SeverityWarning, Title: "Database large",
			Summary: fmt.Sprintf("database is %s (limit %s)", bytesString(uint64(s.DBBytes)), bytesString(t.DBMaxBytes)),
		})
	}
	if t.WALMaxBytes > 0 && s.WALBytes > 0 && uint64(s.WALBytes) > t.WALMaxBytes {
		out = append(out, Finding{
			Key: "wal_size", Severity: SeverityWarning, Title: "Write-ahead log large",
			Summary: fmt.Sprintf("WAL is %s (limit %s)", bytesString(uint64(s.WALBytes)), bytesString(t.WALMaxBytes)),
		})
	}
	return out
}

func sseLagging(s Snapshot, t Thresholds) []Finding {
	if t.LaggedRatio <= 0 || s.SSEStreams == 0 || s.SSELagged == 0 {
		return nil
	}
	ratio := float64(s.SSELagged) / float64(s.SSEStreams)
	if ratio < t.LaggedRatio {
		return nil
	}
	return []Finding{{
		Key: "sse_lagging", Severity: SeverityWarning, Title: "Event subscribers dropping events",
		Summary: fmt.Sprintf("%d of %d event streams lagged (%.0f%%)", s.SSELagged, s.SSEStreams, ratio*100),
	}}
}

func round(d time.Duration) time.Duration { return d.Truncate(time.Second) }

func bytesString(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

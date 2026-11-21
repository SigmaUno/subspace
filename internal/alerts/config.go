package alerts

import (
	"log/slog"
	"strconv"
	"strings"
	"time"
)

const (
	EnvInterval      = "SUBSPACE_ALERTS_INTERVAL"
	EnvStallAfter    = "SUBSPACE_ALERTS_STALL_AFTER"
	EnvProgressAfter = "SUBSPACE_ALERTS_PROGRESS_AFTER"
	EnvReviewAfter   = "SUBSPACE_ALERTS_REVIEW_AFTER"
	EnvPausedMax     = "SUBSPACE_ALERTS_PAUSED_MAX"
	EnvFlappingMax   = "SUBSPACE_ALERTS_FLAPPING_MAX"
	EnvBackupMaxAge  = "SUBSPACE_ALERTS_BACKUP_MAX_AGE"
	EnvDiskMinFree   = "SUBSPACE_ALERTS_DISK_MIN_FREE_BYTES"
	EnvDBMaxBytes    = "SUBSPACE_ALERTS_DB_MAX_BYTES"
	EnvWALMaxBytes   = "SUBSPACE_ALERTS_WAL_MAX_BYTES"
	EnvLaggedRatio   = "SUBSPACE_ALERTS_SSE_LAGGED_RATIO"
)

type Thresholds struct {
	Interval      time.Duration
	StallAfter    time.Duration
	ProgressAfter time.Duration
	ReviewAfter   time.Duration
	PausedMax     int
	FlappingMax   int
	BackupMaxAge  time.Duration
	DiskMinFree   uint64
	DBMaxBytes    uint64
	WALMaxBytes   uint64
	LaggedRatio   float64
}

func Defaults() Thresholds {
	return Thresholds{
		Interval:      30 * time.Second,
		StallAfter:    10 * time.Minute,
		ProgressAfter: 30 * time.Minute,
		ReviewAfter:   30 * time.Minute,
		PausedMax:     3,
		FlappingMax:   1,
		BackupMaxAge:  48 * time.Hour,
		DiskMinFree:   1 << 30,
		DBMaxBytes:    8 << 30,
		WALMaxBytes:   512 << 20,
		LaggedRatio:   0.25,
	}
}

func FromEnv(getenv func(string) string, log *slog.Logger) Thresholds {
	t := Defaults()
	duration := func(key string, dst *time.Duration) {
		v := strings.TrimSpace(getenv(key))
		if v == "" {
			return
		}
		if v == "off" {
			*dst = 0
			return
		}
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			log.Warn("invalid alert setting, using default", "env", key)
			return
		}
		*dst = d
	}
	count := func(key string, dst *int) {
		v := strings.TrimSpace(getenv(key))
		if v == "" {
			return
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			log.Warn("invalid alert setting, using default", "env", key)
			return
		}
		*dst = n
	}
	size := func(key string, dst *uint64) {
		v := strings.TrimSpace(getenv(key))
		if v == "" {
			return
		}
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			log.Warn("invalid alert setting, using default", "env", key)
			return
		}
		*dst = n
	}
	duration(EnvInterval, &t.Interval)
	duration(EnvStallAfter, &t.StallAfter)
	duration(EnvProgressAfter, &t.ProgressAfter)
	duration(EnvReviewAfter, &t.ReviewAfter)
	duration(EnvBackupMaxAge, &t.BackupMaxAge)
	count(EnvPausedMax, &t.PausedMax)
	count(EnvFlappingMax, &t.FlappingMax)
	size(EnvDiskMinFree, &t.DiskMinFree)
	size(EnvDBMaxBytes, &t.DBMaxBytes)
	size(EnvWALMaxBytes, &t.WALMaxBytes)
	if v := strings.TrimSpace(getenv(EnvLaggedRatio)); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || f > 1 {
			log.Warn("invalid alert setting, using default", "env", EnvLaggedRatio)
		} else {
			t.LaggedRatio = f
		}
	}
	return t
}

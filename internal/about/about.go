package about

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/backup"
)

type Backups struct {
	Count          int        `json:"count"`
	LatestAt       *time.Time `json:"latest_at"`
	LatestVerified bool       `json:"latest_verified"`
}

type Info struct {
	Version       string    `json:"version"`
	StartedAt     time.Time `json:"started_at"`
	UptimeSeconds int64     `json:"uptime_seconds"`
	Backend       string    `json:"backend"`
	DBBytes       *int64    `json:"db_bytes"`
	WALBytes      *int64    `json:"wal_bytes"`
	Backups       Backups   `json:"backups"`
}

type Options struct {
	Version string
	DBPath  string
	Started time.Time
	Now     func() time.Time
	Backups func(context.Context) ([]backup.Info, error)
}

func isPostgres(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}

func size(path string) *int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	n := fi.Size()
	return &n
}

func Collect(ctx context.Context, o Options) Info {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	info := Info{
		Version:       o.Version,
		StartedAt:     o.Started.UTC(),
		UptimeSeconds: max(0, int64(now().Sub(o.Started).Seconds())),
		Backend:       "sqlite",
	}
	if isPostgres(o.DBPath) {
		info.Backend = "postgres"
	} else {
		info.DBBytes = size(o.DBPath)
		info.WALBytes = size(o.DBPath + "-wal")
	}
	if o.Backups != nil {
		if list, err := o.Backups(ctx); err == nil && len(list) > 0 {
			info.Backups = Backups{Count: len(list), LatestAt: &list[0].CreatedAt, LatestVerified: list[0].Verified}
		}
	}
	return info
}

func Handler(o Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /about", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Collect(r.Context(), o))
	})
	return mux
}

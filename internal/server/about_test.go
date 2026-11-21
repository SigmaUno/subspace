package server_test

import (
	"testing"

	"github.com/SigmaUno/subspace/internal/servertest"
)

func TestAboutReportsDatabaseAndBackups(t *testing.T) {
	e := servertest.New(t)
	code, out := e.REST("GET", "/api/v1/about", nil)
	if code != 200 {
		t.Fatalf("about: %d %v", code, out)
	}
	if out["backend"] != "sqlite" || out["version"] == "" || out["uptime_seconds"].(float64) < 0 {
		t.Fatalf("about: %v", out)
	}
	if out["db_bytes"] == nil || out["db_bytes"].(float64) <= 0 {
		t.Fatalf("db_bytes: %v", out["db_bytes"])
	}
	if got := out["backups"].(map[string]any)["count"].(float64); got != 0 {
		t.Fatalf("backups before: %v", out["backups"])
	}

	if code, out := e.REST("POST", "/api/v1/backup", map[string]any{}); code != 200 {
		t.Fatalf("backup: %d %v", code, out)
	}
	_, out = e.REST("GET", "/api/v1/about", nil)
	backups := out["backups"].(map[string]any)
	if backups["count"].(float64) != 1 || backups["latest_verified"] != true || backups["latest_at"] == nil {
		t.Fatalf("backups after: %v", backups)
	}
}

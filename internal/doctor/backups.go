package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

const (
	defaultBackupInterval = 24 * time.Hour
	backupTimeLayout      = "20060102T150405Z"
)

var backupName = regexp.MustCompile(`^subspace-(\d{8}T\d{6}Z)\.db$`)

func checkBackups(ctx context.Context, e Env) []Result {
	const id = "backups.latest"
	path := dbPath(e)
	if isPostgres(path) {
		return nil
	}
	dir := getenv(e, "SUBSPACE_BACKUP_DIR", filepath.Join(filepath.Dir(path), "backups"))
	interval := defaultBackupInterval
	if v := e.Getenv("SUBSPACE_BACKUP_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil
		}
		interval = d
	}
	const fix = "run: subspace backup, and keep SUBSPACE_BACKUP_INTERVAL above 0"
	names, err := backupNames(dir)
	if err != nil {
		return result(Error, id, "cannot list %s: %v", "check the permissions of the backup directory", dir, err)
	}
	if len(names) == 0 {
		return result(Warn, id, "no backups in %s", fix, dir)
	}
	latest := names[len(names)-1]
	created, _ := time.Parse(backupTimeLayout, backupName.FindStringSubmatch(latest)[1])
	age := e.Now().UTC().Sub(created)
	if err := sqlite.IntegrityCheck(ctx, filepath.Join(dir, latest)); err != nil {
		return result(Error, id, "latest backup %s fails verification: %v", fix, latest, err)
	}
	base := interval
	if base <= 0 {
		base = defaultBackupInterval
	}
	age = age.Round(time.Second)
	switch {
	case age > 7*base:
		return result(Error, id, "latest backup %s is %s old and verified", fix, latest, age)
	case age > 2*base:
		return result(Warn, id, "latest backup %s is %s old and verified", fix, latest, age)
	}
	return result(OK, id, "latest backup %s is %s old and verified", "", latest, age)
}

func backupNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, ent := range entries {
		if ent.Type().IsRegular() && backupName.MatchString(ent.Name()) {
			names = append(names, ent.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

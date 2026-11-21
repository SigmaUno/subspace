package server

import (
	"errors"
	"fmt"

	"github.com/SigmaUno/subspace/internal/backup/offsite"
)

type offsiteSetup struct {
	Cfg    offsite.Config
	Remote *offsite.Remote
}

func init() { registerMount(mountOffsite) }

func openOffsite(env func(string) string, backupDir string) (*offsiteSetup, error) {
	cfg, err := offsite.ConfigFromEnv(env)
	if err != nil {
		return nil, fmt.Errorf("off-site backups: %w", err)
	}
	if !cfg.Enabled() {
		return nil, nil
	}
	if backupDir == "" {
		return nil, errors.New("off-site backups: need SUBSPACE_BACKUP_DIR")
	}
	remote, err := offsite.Open(cfg, offsite.Deps{Getenv: env})
	if err != nil {
		return nil, fmt.Errorf("off-site backups: %w", err)
	}
	return &offsiteSetup{Cfg: cfg, Remote: remote}, nil
}

func mountOffsite(d Deps) {
	if d.Offsite == nil || d.Backups == nil {
		return
	}
	svc, err := offsite.NewService(offsite.Options{
		Remote: d.Offsite.Remote, Store: d.Store, Backups: backupLister(d.Board.ListBackups),
		StageDir: d.Cfg.BackupDir, Retention: d.Offsite.Cfg.Retention, Interval: d.Offsite.Cfg.Interval,
		Now: d.Cfg.Now, Log: d.Log,
	})
	if err != nil {
		d.Log.Error("off-site backups disabled", "err", err)
		return
	}
	d.Backups.SetAnnotator(svc.Annotate)
	d.Leader.Go(d.Ctx, svc.Run)
}

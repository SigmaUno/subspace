package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/SigmaUno/subspace/internal/artifacts"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const artifactIndexKey = "artifacts.index"

func init() {
	registerMount(func(d Deps) {
		cfg, err := artifactsConfig(d)
		if err != nil {
			d.Log.Error("artifacts disabled", "err", err)
			return
		}
		svc, err := artifacts.New(cfg)
		if err != nil {
			d.Log.Error("artifacts disabled", "err", err)
			return
		}
		h := svc.Handler()
		d.API.Mount(d.Mux, "/api/v1/artifacts", h)
		d.API.Mount(d.Mux, "/api/v1/artifacts/", h)
		mcp.BindArtifacts(d.Board, svc.Tools())
	})
}

func artifactsConfig(d Deps) (artifacts.Config, error) {
	dir := os.Getenv("SUBSPACE_DATA_DIR")
	if dir == "" && isPostgres(d.Cfg.DBPath) {
		return artifacts.Config{}, errors.New("SUBSPACE_DATA_DIR is required with the postgres backend")
	}
	if dir == "" {
		dir = filepath.Dir(d.Cfg.DBPath)
	}
	maxFile, err := envBytes("SUBSPACE_ARTIFACT_MAX_FILE_BYTES")
	if err != nil {
		return artifacts.Config{}, err
	}
	quota, err := envBytes("SUBSPACE_ARTIFACT_PROJECT_QUOTA_BYTES")
	if err != nil {
		return artifacts.Config{}, err
	}
	return artifacts.Config{
		Root:         filepath.Join(dir, "artifacts"),
		MaxFileSize:  maxFile,
		ProjectQuota: quota,
		Store:        artifactIndex{d.Store},
		Directory:    artifactDirectory{d.Store},
		Auditor:      artifactAuditor{d.Store},
		Logger:       d.Log,
	}, nil
}

func envBytes(name string) (int64, error) {
	v := os.Getenv(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return n, nil
}

type artifactDirectory struct{ st store.Store }

func (d artifactDirectory) Task(ctx context.Context, id string) (t *model.Task, err error) {
	err = d.st.View(ctx, func(tx store.Tx) (e error) { t, e = tx.GetTask(id); return })
	return
}

func (d artifactDirectory) Brief(ctx context.Context, id string) (b *model.Brief, err error) {
	err = d.st.View(ctx, func(tx store.Tx) (e error) { b, e = tx.GetBrief(id); return })
	return
}

func (d artifactDirectory) Agent(ctx context.Context, id string) (a *model.Agent, err error) {
	err = d.st.View(ctx, func(tx store.Tx) (e error) { a, e = tx.GetAgent(id); return })
	return
}

func (d artifactDirectory) AgentTasks(ctx context.Context, agentID string) ([]*model.Task, error) {
	var out []*model.Task
	err := d.st.View(ctx, func(tx store.Tx) error {
		for _, f := range []store.TaskFilter{{Assignee: agentID}, {Reviewer: agentID}} {
			ts, err := tx.ListTasks(f)
			if err != nil {
				return err
			}
			out = append(out, ts...)
		}
		return nil
	})
	return out, err
}

type artifactAuditor struct{ st store.Store }

func (a artifactAuditor) Record(ctx context.Context, e *model.AuditEvent) error {
	return a.st.Atomic(ctx, func(tx store.Tx) error { return tx.InsertAudit(e) })
}

type artifactIndexDoc struct {
	Seq   int                   `json:"seq"`
	Items []*artifacts.Artifact `json:"items"`
}

type artifactIndex struct{ st store.Store }

func (x artifactIndex) load(tx store.Tx) (artifactIndexDoc, error) {
	var doc artifactIndexDoc
	raw, ok, err := tx.GetKV(artifactIndexKey)
	if err != nil || !ok {
		return doc, err
	}
	return doc, json.Unmarshal([]byte(raw), &doc)
}

func (x artifactIndex) read(ctx context.Context, fn func(artifactIndexDoc) error) error {
	return x.st.View(ctx, func(tx store.Tx) error {
		doc, err := x.load(tx)
		if err != nil {
			return err
		}
		return fn(doc)
	})
}

func (x artifactIndex) update(ctx context.Context, fn func(*artifactIndexDoc) error) error {
	return x.st.Atomic(ctx, func(tx store.Tx) error {
		doc, err := x.load(tx)
		if err != nil {
			return err
		}
		if err := fn(&doc); err != nil {
			return err
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		return tx.SetKV(artifactIndexKey, string(raw))
	})
}

func (x artifactIndex) Insert(ctx context.Context, a *artifacts.Artifact) error {
	return x.update(ctx, func(doc *artifactIndexDoc) error {
		doc.Seq++
		a.ID = fmt.Sprintf("A-%d", doc.Seq)
		doc.Items = append(doc.Items, a)
		return nil
	})
}

func (x artifactIndex) Get(ctx context.Context, id string) (a *artifacts.Artifact, err error) {
	err = x.read(ctx, func(doc artifactIndexDoc) error {
		i := slices.IndexFunc(doc.Items, func(it *artifacts.Artifact) bool { return it.ID == id })
		if i < 0 {
			return store.ErrNotFound
		}
		a = doc.Items[i]
		return nil
	})
	return
}

func (x artifactIndex) List(ctx context.Context, f artifacts.Filter) (out []*artifacts.Artifact, err error) {
	err = x.read(ctx, func(doc artifactIndexDoc) error {
		for _, it := range doc.Items {
			if len(f.Scopes) > 0 && !slices.Contains(f.Scopes, it.Scope) {
				continue
			}
			out = append(out, it)
			if f.Limit > 0 && len(out) >= f.Limit {
				break
			}
		}
		return nil
	})
	return
}

func (x artifactIndex) Delete(ctx context.Context, id string) error {
	return x.update(ctx, func(doc *artifactIndexDoc) error {
		i := slices.IndexFunc(doc.Items, func(it *artifacts.Artifact) bool { return it.ID == id })
		if i < 0 {
			return store.ErrNotFound
		}
		doc.Items = slices.Delete(doc.Items, i, i+1)
		return nil
	})
}

func (x artifactIndex) ProjectUsage(ctx context.Context, project string) (n int64, err error) {
	err = x.read(ctx, func(doc artifactIndexDoc) error {
		for _, it := range doc.Items {
			if it.Project == project {
				n += it.Size
			}
		}
		return nil
	})
	return
}

func (x artifactIndex) CountBySHA(ctx context.Context, sha string) (n int, err error) {
	err = x.read(ctx, func(doc artifactIndexDoc) error {
		for _, it := range doc.Items {
			if it.SHA256 == sha {
				n++
			}
		}
		return nil
	})
	return
}

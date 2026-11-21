package activity

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	Topic     = "activity"
	EventType = "activity.new"

	tailBatch = 500
)

type Tailer struct {
	st  store.Store
	pub events.Publisher
	log *slog.Logger

	auditPos int64
	msgPos   int64
}

func NewTailer(st store.Store, pub events.Publisher, log *slog.Logger) *Tailer {
	return &Tailer{st: st, pub: pub, log: log}
}

func (t *Tailer) Run(ctx context.Context, every time.Duration) {
	if err := t.poll(ctx, false); err != nil {
		t.log.Error("activity tail start", "err", err)
		return
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := t.poll(ctx, true); err != nil && ctx.Err() == nil {
				t.log.Error("activity tail", "err", err)
			}
		}
	}
}

func (t *Tailer) poll(ctx context.Context, publish bool) error {
	var items []entry
	err := t.st.View(ctx, func(tx store.Tx) error {
		at, ok := tx.(store.ActivityTx)
		if !ok {
			return errors.New("store does not support activity")
		}
		limit := tailBatch
		if !publish {
			limit = 1
		}
		res := newResolver(tx)
		evs, err := at.AuditNewest(store.ActivityRange{After: t.auditPos, Limit: limit})
		if err != nil {
			return err
		}
		for _, e := range evs {
			it := FromAudit(e)
			it.BriefID = res.auditBrief(e)
			items = append(items, entry{it, e.ID})
			t.auditPos = max(t.auditPos, e.ID)
		}
		ms, err := at.SystemMessagesNewest(store.ActivityRange{After: t.msgPos, Limit: limit})
		if err != nil {
			return err
		}
		for _, m := range ms {
			it := FromMessage(m)
			it.BriefID = res.messageBrief(m)
			items = append(items, entry{it, m.ID})
			t.msgPos = max(t.msgPos, m.ID)
		}
		return nil
	})
	if err != nil || !publish {
		return err
	}
	slices.SortFunc(items, func(a, b entry) int {
		return cmp.Or(a.TS.Compare(b.TS), cmp.Compare(a.Source, b.Source), cmp.Compare(a.seq, b.seq))
	})
	for _, e := range items {
		t.pub.Publish([]string{Topic}, EventType, e.Item)
	}
	return nil
}

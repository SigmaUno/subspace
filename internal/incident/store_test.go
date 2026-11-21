package incident_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"

	"github.com/SigmaUno/subspace/internal/incident"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/faultstore"
	"github.com/SigmaUno/subspace/internal/testdb"
)

type plainStore struct{ store.Store }

type plainTx struct{ store.Tx }

func (s plainStore) Atomic(ctx context.Context, fn func(store.Tx) error) error {
	return s.Store.Atomic(ctx, func(tx store.Tx) error { return fn(plainTx{tx}) })
}

func (s plainStore) View(ctx context.Context, fn func(store.Tx) error) error {
	return s.Store.View(ctx, func(tx store.Tx) error { return fn(plainTx{tx}) })
}

func execSQL(t *testing.T, st store.Store, q string, args ...any) {
	t.Helper()
	err := st.Atomic(context.Background(), func(tx store.Tx) error {
		_, err := tx.(interface {
			Exec(string, ...any) (sql.Result, error)
		}).Exec(q, args...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStoreWithoutSQL(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if !incident.SupportsSQL(ctx, st) || incident.SupportsSQL(ctx, plainStore{st}) {
		t.Fatal("SupportsSQL misreports")
	}
	svc := newService(t, plainStore{st}, &fakeControl{}, nil, "")
	if _, err := svc.Start(ctx, incident.Request{Reason: "x", Actions: []string{incident.Notify}}); err == nil {
		t.Fatal("Start succeeded without SQL")
	}
	if _, err := svc.List(ctx, 10); err == nil {
		t.Fatal("List succeeded without SQL")
	}
	if _, err := svc.Get(ctx, "INC-1"); err == nil {
		t.Fatal("Get succeeded without SQL")
	}
}

func TestCorruptRowsSurfaceErrors(t *testing.T) {
	for _, tc := range []struct {
		name, actions, results string
		started                any
	}{
		{"actions", `{`, `[]`, 1000},
		{"results", `[]`, `["notify"]`, 1000},
		{"started_at", `[]`, `[]`, "yesterday"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, text := tc.started.(string); text && testdb.Postgres() {
				t.Skip("postgres rejects non-integer timestamps on insert")
			}
			st := openStore(t)
			execSQL(t, st, `INSERT INTO incidents (id, reason, actions, results, started_at, ended_at, actor) VALUES ('INC-1', 'x', ?, ?, ?, 0, 'owner')`,
				tc.actions, tc.results, tc.started)
			svc := newService(t, st, &fakeControl{}, nil, "")
			if in, err := svc.Get(context.Background(), "INC-1"); err == nil {
				t.Fatalf("Get returned %+v", in)
			}
			if _, err := svc.List(context.Background(), 10); err == nil {
				t.Fatal("List succeeded")
			}
		})
	}
}

func TestStartStoreFaultsLeaveNoIncident(t *testing.T) {
	for op := int64(1); op <= 5; op++ {
		st := openStore(t)
		fs := faultstore.New(st, 1, faultstore.Faults{FailAt: []int64{op}})
		ctl := &fakeControl{}
		pub := &recorder{}
		svc := newService(t, fs, ctl, pub, "")
		_, err := svc.Start(context.Background(), incident.Request{Reason: "x", Actions: []string{incident.PauseAll, incident.Notify}})
		if !errors.Is(err, faultstore.ErrInjected) {
			t.Fatalf("op %d: err = %v", op, err)
		}
		if ctl.paused || len(pub.events) != 0 {
			t.Fatalf("op %d: actions ran after a failed start", op)
		}
		if list, err := svc.List(context.Background(), 10); err != nil || len(list) != 0 {
			t.Fatalf("op %d: list %v %v", op, list, err)
		}
		if ev := audits(t, st, "incident_start"); len(ev) != 0 {
			t.Fatalf("op %d: audit %+v", op, ev)
		}
	}
}

func TestStartMergeFaultKeepsActiveIncident(t *testing.T) {
	ctx := context.Background()
	fs := faultstore.New(openStore(t), 1, faultstore.Faults{})
	ctl := &fakeControl{}
	svc := newService(t, fs, ctl, nil, "")
	if _, err := svc.Start(ctx, incident.Request{Reason: "x", Actions: []string{incident.Notify}}); err != nil {
		t.Fatal(err)
	}
	fs.Reset(1)
	fs.Set(faultstore.Faults{FailAt: []int64{2}})
	if _, err := svc.Start(ctx, incident.Request{Reason: "x", Actions: []string{incident.PauseAll}}); !errors.Is(err, faultstore.ErrInjected) {
		t.Fatalf("err = %v", err)
	}
	fs.Set(faultstore.Faults{})
	got, err := svc.Get(ctx, "INC-1")
	if err != nil {
		t.Fatal(err)
	}
	if ctl.paused || !slices.Equal(got.Actions, []string{incident.Notify}) {
		t.Fatalf("paused=%v actions=%v", ctl.paused, got.Actions)
	}
}

func TestResultRecordingFaultKeepsIncidentReadable(t *testing.T) {
	ctx := context.Background()
	fs := faultstore.New(openStore(t), 1, faultstore.Faults{FailAt: []int64{6}})
	svc := newService(t, fs, &fakeControl{}, nil, "")
	out, err := svc.Start(ctx, incident.Request{Reason: "x", Actions: []string{incident.Notify}})
	if err != nil {
		t.Fatal(err)
	}
	if ev := fs.Injected(); len(ev) != 1 || ev[0].Method != "Exec" {
		t.Fatalf("injected %+v", ev)
	}
	if out.Succeeded != 1 {
		t.Fatalf("outcome %+v", out)
	}
	got, err := svc.Get(ctx, out.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Actions, []string{incident.Notify}) || len(got.Results) != 0 || !got.StartedAt.Equal(clock) || got.EndedAt != nil {
		t.Fatalf("stored %+v", got)
	}
}

func TestEndStoreFaultsKeepIncidentActive(t *testing.T) {
	ctx := context.Background()
	for op := int64(1); op <= 3; op++ {
		fs := faultstore.New(openStore(t), 1, faultstore.Faults{})
		svc := newService(t, fs, &fakeControl{}, nil, "")
		out, err := svc.Start(ctx, incident.Request{Reason: "x", Actions: []string{incident.Notify}})
		if err != nil {
			t.Fatal(err)
		}
		fs.Reset(1)
		fs.Set(faultstore.Faults{FailAt: []int64{op}})
		if _, err := svc.End(ctx, out.ID, incident.EndRequest{}); !errors.Is(err, faultstore.ErrInjected) {
			t.Fatalf("op %d: err = %v", op, err)
		}
		fs.Set(faultstore.Faults{})
		got, err := svc.Get(ctx, out.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.EndedAt != nil {
			t.Fatalf("op %d: ended despite failure", op)
		}
	}
}

func TestReadFaultsSurface(t *testing.T) {
	ctx := context.Background()
	fs := faultstore.New(openStore(t), 1, faultstore.Faults{})
	svc := newService(t, fs, &fakeControl{}, nil, "")
	if _, err := svc.Start(ctx, incident.Request{Reason: "x", Actions: []string{incident.Notify}}); err != nil {
		t.Fatal(err)
	}
	fs.Set(faultstore.Faults{Reads: true, ErrorRate: 1})
	if _, err := svc.List(ctx, 10); !errors.Is(err, faultstore.ErrInjected) {
		t.Fatalf("List err = %v", err)
	}
	if _, err := svc.Get(ctx, "INC-1"); !errors.Is(err, faultstore.ErrInjected) {
		t.Fatalf("Get err = %v", err)
	}
}

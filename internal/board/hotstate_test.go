package board

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

func TestHotStateDoesNotGrowWithAgentChurn(t *testing.T) {
	ctx := context.Background()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b, err := New(ctx, st, Options{Now: func() time.Time { return now }, FlapWindow: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	const n = 200
	for i := range n {
		id := fmt.Sprintf("w%d", i)
		if _, err := b.RegisterAgent(ctx, AgentInput{ID: id, Role: model.RoleWorker, Kind: model.KindClaudeCode}); err != nil {
			t.Fatal(err)
		}
		b.Touch(id)
		b.recordExpiry(id)
		now = now.Add(time.Minute)
		if i%10 == 0 {
			b.Flush(ctx)
		}
	}
	b.Flush(ctx)

	a, err := b.GetAgent(ctx, "w0")
	if err != nil {
		t.Fatal(err)
	}
	if a.LastSeenAt == nil || !a.LastSeenAt.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("w0 last_seen_at = %v after flush", a.LastSeenAt)
	}
	b.mu.Lock()
	seen, exp := len(b.seen), len(b.expiries)
	b.mu.Unlock()
	if seen != 0 {
		t.Errorf("%d last-seen entries kept after flush", seen)
	}
	if exp > 61 {
		t.Errorf("%d agents tracked for flapping, want at most the 61 inside the window", exp)
	}
}

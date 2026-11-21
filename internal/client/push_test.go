package client_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/model"
)

func nextNotification(t *testing.T, ch <-chan client.Notification, within time.Duration) client.Notification {
	t.Helper()
	select {
	case n, ok := <-ch:
		if !ok {
			t.Fatal("notifications closed")
		}
		return n
	case <-time.After(within):
		t.Fatal("no notification")
	}
	return client.Notification{}
}

func TestNotificationsWakeAnIdleWorker(t *testing.T) {
	defer guard(t)
	e := newEnv(t)
	tm := e.team()
	br := e.brief()
	task := e.readyTask(tm.leader, br.ID, "wake")

	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	ch := tm.worker.Notifications(ctx)
	if ok := must(tm.worker.StreamAvailable(ctx)); !ok {
		t.Fatal("stream not available")
	}
	time.Sleep(100 * time.Millisecond)

	_ = must(tm.leader.Assign(ctx, task.ID, "wally", ""))
	n := nextNotification(t, ch, 2*time.Second)
	ev, ok := n.Event()
	if !ok || ev.Type != "task_assigned" || ev.TaskID != task.ID || n.ID == "" {
		t.Fatalf("notification %+v event %+v", n, ev)
	}

	if err := tm.worker.Subscribe(ctx, "subspace://task/"+task.ID); err != nil {
		t.Fatal(err)
	}
	if err := tm.reviewer.Subscribe(ctx, "subspace://task/"+task.ID); client.CodeOf(err) != "rpc-32002" {
		t.Fatalf("foreign subscribe: %v", err)
	}
	_ = must(tm.worker.ClaimTask(ctx, task.ID))
	n = nextNotification(t, ch, 2*time.Second)
	if uri, ok := n.ResourceURI(); !ok || uri != "subspace://task/"+task.ID {
		t.Fatalf("notification %+v", n)
	}
}

type cutProxy struct {
	ln      net.Listener
	target  string
	mu      sync.Mutex
	blocked bool
	conns   []net.Conn
}

func newCutProxy(t *testing.T, target string) *cutProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &cutProxy{ln: ln, target: target}
	t.Cleanup(func() { _ = ln.Close(); p.cut(false) })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			blocked := p.blocked
			p.mu.Unlock()
			up, derr := net.Dial("tcp", target)
			if blocked || derr != nil {
				_ = c.Close()
				if up != nil {
					_ = up.Close()
				}
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, c, up)
			p.mu.Unlock()
			go func() { _, _ = io.Copy(up, c); _ = up.Close() }()
			go func() { _, _ = io.Copy(c, up); _ = c.Close() }()
		}
	}()
	return p
}

func (p *cutProxy) cut(block bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blocked = block
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.conns = nil
}

func TestNotificationsReconnectAndResumeWithoutLoss(t *testing.T) {
	defer guard(t)
	e := newEnv(t)
	reg := must(e.owner.RegisterAgent(e.ctx, board.AgentInput{ID: "wally", Role: model.RoleWorker, Kind: model.KindClaudeCode, Model: "m"}))

	u := must(url.Parse(e.url))
	proxy := newCutProxy(t, u.Host)
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	worker := client.NewMCP("http://"+proxy.ln.Addr().String()+"/mcp", reg.Connect.Token, client.WithSleep(func(ctx context.Context, _ time.Duration) error {
		select {
		case <-time.After(20 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	if err := worker.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	ch := worker.Notifications(ctx)
	time.Sleep(100 * time.Millisecond)

	say := func(n int) {
		_ = must(e.owner.PostThreadMessage(ctx, model.ThreadAgent, "wally", string(rune('a'+n))))
	}
	say(0)
	first := nextNotification(t, ch, 2*time.Second)

	proxy.cut(true)
	say(1)
	say(2)
	time.Sleep(100 * time.Millisecond)
	proxy.cut(false)

	var ids []int64
	for range 2 {
		ev, ok := nextNotification(t, ch, 3*time.Second).Event()
		if !ok || ev.Type != "message" {
			t.Fatalf("event %+v", ev)
		}
		ids = append(ids, ev.MessageID)
	}
	f, _ := first.Event()
	if ids[0] != f.MessageID+1 || ids[1] != f.MessageID+2 {
		t.Fatalf("first %d, replayed %v", f.MessageID, ids)
	}
}

func TestNotificationsReportUnsupportedServers(t *testing.T) {
	t.Setenv("SUBSPACE_MCP_PUSH", "off")
	defer guard(t)
	e := newEnv(t)
	tm := e.team()
	ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	defer cancel()
	if ok := must(tm.worker.StreamAvailable(ctx)); ok {
		t.Fatal("stream reported available")
	}
	ch := tm.worker.Notifications(ctx)
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("unexpected notification")
		}
	case <-ctx.Done():
		t.Fatal("channel not closed")
	}
	if err := tm.worker.NotificationsErr(); !errors.Is(err, client.ErrStreamUnsupported) {
		t.Fatalf("err %v", err)
	}
}

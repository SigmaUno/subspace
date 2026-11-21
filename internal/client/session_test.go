package client_test

import (
	"net/http"
	"testing"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/model"
)

func TestSessionLifecycle(t *testing.T) {
	defer guard(t)
	e := newEnv(t)
	ctx := e.ctx
	reg := must(e.owner.RegisterAgent(ctx, board.AgentInput{ID: "wally", Role: model.RoleWorker, Kind: model.KindClaudeCode, Model: "m"}))
	token := reg.Connect.Token

	lazy := client.NewMCP(e.url+"/mcp", token)
	if _, err := lazy.WhoAmI(ctx); err != nil || lazy.SessionID() != "" {
		t.Fatalf("call without initialize: err=%v session=%q", err, lazy.SessionID())
	}

	c := e.mcp(token)
	first := c.SessionID()
	if first == "" {
		t.Fatal("no session id after initialize")
	}

	del, _ := http.NewRequestWithContext(ctx, http.MethodDelete, e.url+"/mcp", nil)
	del.Header.Set("Authorization", "Bearer "+token)
	del.Header.Set("Mcp-Session-Id", first)
	resp, err := http.DefaultClient.Do(del)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("server-side delete: %d", resp.StatusCode)
	}

	if me := must(c.WhoAmI(ctx)); me.Agent.ID != "wally" {
		t.Fatalf("whoami after expiry: %+v", me)
	}
	second := c.SessionID()
	if second == "" || second == first {
		t.Fatalf("session not renewed: %q -> %q", first, second)
	}

	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if c.SessionID() != "" {
		t.Fatal("session id kept after close")
	}
	if err := c.Close(ctx); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

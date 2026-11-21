package server_test

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"testing"

	"github.com/SigmaUno/subspace/internal/servertest"
)

func peerDo(t *testing.T, peer string, req *http.Request) (int, []byte) {
	t.Helper()
	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(peer)}}
	cl := &http.Client{Transport: &http.Transport{DialContext: dialer.DialContext}}
	defer cl.CloseIdleConnections()
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestAgentAllowlistCoversIdempotentReplay(t *testing.T) {
	needLoopbackPeer(t, "127.0.0.2")
	t.Setenv("SUBSPACE_AGENT_ALLOW_CIDRS", "127.0.0.1")
	e := servertest.New(t)
	tok := registerAgentToken(t, e, "w1")
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"add_note","arguments":{"body":"secret plan"},"_meta":{"idempotencyKey":"k1"}}}`)
	send := func(peer string) (int, []byte) {
		req, _ := http.NewRequest(http.MethodPost, e.TS.URL+"/mcp", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		return peerDo(t, peer, req)
	}
	if code, out := send("127.0.0.1"); code != http.StatusOK {
		t.Fatalf("allowed peer: %d %s", code, out)
	}
	if code, out := send("127.0.0.2"); code != http.StatusForbidden {
		t.Fatalf("replay from unlisted peer: %d %s", code, out)
	}
}

func TestOwnerAllowlistCoversSpawnListing(t *testing.T) {
	needLoopbackPeer(t, "127.0.0.2")
	t.Setenv("SUBSPACE_OWNER_ALLOW_CIDRS", "127.0.0.1")
	e := servertest.New(t)
	req, _ := http.NewRequest(http.MethodGet, e.TS.URL+"/api/v1/spawns", nil)
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	if code, out := peerDo(t, "127.0.0.2", req); code != http.StatusForbidden {
		t.Fatalf("spawn listing from unlisted peer: %d %s", code, out)
	}
}

package gateway

import (
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestStdioReaderSurvivesDuplicateResponses(t *testing.T) {
	_, stdin := io.Pipe()
	p := &stdioProc{cmd: &exec.Cmd{}, stdin: stdin, pending: map[int64]chan *rpcMessage{}, done: make(chan struct{})}
	p.pending[1] = make(chan *rpcMessage, 1)
	line := `{"jsonrpc":"2.0","id":1,"result":{"content":[]}}` + "\n"
	go p.read(strings.NewReader(strings.Repeat(line, 3)), 1<<20)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("reader blocked on a duplicate response for an id nobody waits on")
	}
}

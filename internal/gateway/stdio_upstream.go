package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

const (
	stdioPath       = "/usr/local/bin:/usr/bin:/bin"
	restartBase     = time.Second
	restartMax      = 30 * time.Second
	healthyRun      = time.Minute
	startTimeout    = 15 * time.Second
	stdioLineReserv = 64 << 10
)

type stdioUpstream struct {
	command string
	args    []string
	credEnv string
	secret  string
	maxLine int64
	now     func() time.Time

	mu        sync.Mutex
	proc      *stdioProc
	failures  int
	retryAt   time.Time
	closed    bool
	startedAt time.Time
}

type stdioProc struct {
	cmd     *exec.Cmd
	dir     string
	stdin   io.WriteCloser
	wmu     sync.Mutex
	mu      sync.Mutex
	next    int64
	pending map[int64]chan *rpcMessage
	done    chan struct{}
	err     error
	once    sync.Once
}

func newStdioUpstream(g Gateway, secret string, now func() time.Time) *stdioUpstream {
	return &stdioUpstream{command: g.Command, args: g.Args, credEnv: g.CredentialEnv, secret: secret,
		maxLine: int64(g.MaxResultBytes) + stdioLineReserv, now: now}
}

func (s *stdioUpstream) env(dir string) []string {
	env := []string{"PATH=" + stdioPath, "HOME=" + dir, "TMPDIR=" + dir, "LANG=C.UTF-8"}
	if s.credEnv != "" && s.secret != "" {
		env = append(env, s.credEnv+"="+s.secret)
	}
	return env
}

func (s *stdioUpstream) close() {
	s.mu.Lock()
	s.closed = true
	p := s.proc
	s.proc = nil
	s.mu.Unlock()
	if p != nil {
		p.kill()
	}
}

func (s *stdioUpstream) current(ctx context.Context) (*stdioProc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errUnavailable
	}
	if s.proc != nil {
		select {
		case <-s.proc.done:
			if s.now().Sub(s.startedAt) >= healthyRun {
				s.failures = 0
			}
			s.failures++
			s.retryAt = s.now().Add(backoff(s.failures))
			s.proc = nil
		default:
			return s.proc, nil
		}
	}
	if s.now().Before(s.retryAt) {
		return nil, errors.Join(errUnavailable, errors.New("upstream process is restarting"))
	}
	p, err := s.start(ctx)
	if err != nil {
		s.failures++
		s.retryAt = s.now().Add(backoff(s.failures))
		return nil, err
	}
	s.proc, s.startedAt = p, s.now()
	return p, nil
}

func backoff(n int) time.Duration {
	d := restartBase
	for i := 1; i < n && d < restartMax; i++ {
		d *= 2
	}
	return min(d, restartMax)
}

func (s *stdioUpstream) start(ctx context.Context) (*stdioProc, error) {
	dir, err := os.MkdirTemp("", "subspace-gw-")
	if err != nil {
		return nil, errors.Join(errUnavailable, err)
	}
	cmd := exec.Command(s.command, s.args...) //nolint:gosec // command is checked against the owner-configured allowlist
	cmd.Env = s.env(dir)
	cmd.Dir = dir
	setGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, errors.Join(errUnavailable, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, errors.Join(errUnavailable, err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, errors.Join(errUnavailable, errors.New("cannot start upstream command"))
	}
	p := &stdioProc{cmd: cmd, dir: dir, stdin: stdin, pending: map[int64]chan *rpcMessage{}, done: make(chan struct{})}
	go p.read(stdout, s.maxLine)
	ictx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	if _, err := p.call(ictx, "initialize", initParams()); err != nil {
		p.kill()
		return nil, err
	}
	if err := p.send(rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"}); err != nil {
		p.kill()
		return nil, err
	}
	return p, nil
}

func (s *stdioUpstream) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	p, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	res, err := p.call(ctx, method, params)
	if errors.Is(err, errTooLarge) {
		p.kill()
	}
	return res, err
}

func (p *stdioProc) send(msg rpcRequest) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	if _, err := p.stdin.Write(append(b, '\n')); err != nil {
		return errors.Join(errUnavailable, err)
	}
	return nil
}

func (p *stdioProc) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	ch := make(chan *rpcMessage, 1)
	p.mu.Lock()
	if p.err != nil {
		err := p.err
		p.mu.Unlock()
		return nil, err
	}
	p.next++
	id := p.next
	p.pending[id] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
	}()
	if err := p.send(rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		return nil, err
	}
	select {
	case m := <-ch:
		return m.outcome()
	case <-p.done:
		p.mu.Lock()
		err := p.err
		p.mu.Unlock()
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *stdioProc) read(stdout io.Reader, maxLine int64) {
	r := bufio.NewReaderSize(stdout, 64<<10)
	var err error
	for {
		var line string
		line, err = readLine(r, maxLine)
		if err != nil {
			break
		}
		var m rpcMessage
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		if m.Method != "" {
			if len(m.ID) > 0 && string(m.ID) != "null" {
				p.reject(m.ID)
			}
			continue
		}
		id, ok := idOf(m.ID)
		if !ok {
			continue
		}
		p.mu.Lock()
		ch := p.pending[id]
		p.mu.Unlock()
		if ch != nil {
			ch <- &m
		}
	}
	if !errors.Is(err, errTooLarge) {
		err = errors.Join(errUnavailable, errors.New("upstream process exited"))
	}
	p.mu.Lock()
	p.err = err
	p.mu.Unlock()
	p.kill()
	close(p.done)
}

func (p *stdioProc) reject(id json.RawMessage) {
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "method not found"}})
	if err != nil {
		return
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_, _ = p.stdin.Write(append(b, '\n'))
}

func (p *stdioProc) kill() {
	p.mu.Lock()
	if p.err == nil {
		p.err = errors.Join(errUnavailable, errors.New("upstream process stopped"))
	}
	p.mu.Unlock()
	p.once.Do(func() {
		_ = p.stdin.Close()
		_ = killGroup(p.cmd)
		go func() {
			<-p.done
			_ = p.cmd.Wait()
			_ = os.RemoveAll(p.dir)
		}()
	})
}

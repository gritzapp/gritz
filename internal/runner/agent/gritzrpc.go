package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/gritzapp/gritz/internal/runner/agent/gritzrpc"
	"github.com/gritzapp/gritz/internal/x/lines"
)

const (
	// gritzrpcCancelGrace is how long a cancelled or failed run waits after
	// run/cancel before it sends SIGTERM to the agent's process group.
	gritzrpcCancelGrace = 10 * time.Second
	// gritzrpcExitGrace is how long the agent may keep running after it
	// responded to run/start before it is sent SIGTERM.
	gritzrpcExitGrace = 5 * time.Second
	// gritzrpcKillGrace is how long SIGTERM has before SIGKILL, and how long
	// stdout and stderr are drained after the agent exits.
	gritzrpcKillGrace = 5 * time.Second
	// gritzrpcMaxLineInError bounds the invalid stdout line quoted in an error.
	gritzrpcMaxLineInError = 256
)

// startID is the id of the only request gritz sends, run/start.
const startID = int64(1)

// GritzRPCAgent implements Agent by speaking gritzrpc to a spawned process.
//
// One process runs per run. It receives one run/start request on stdin, may
// stream log notifications on stdout, and its response to run/start ends the
// run. Stderr is teed raw into the driver log.
type GritzRPCAgent struct {
	log        *DriverLog
	cwd        string
	env        []string
	mcpServers map[string]McpServer
	options    *GritzRPCOptions
	// cancelGrace is gritzrpcCancelGrace; tests shorten it.
	cancelGrace time.Duration
}

// Prompt spawns the agent, sends run/start and waits for its response.
//
// Prompt returns once the process has exited. When ctx is cancelled the agent
// is sent run/cancel and, if it does not exit in time, SIGTERM and then
// SIGKILL; the run then counts as cancelled whatever the agent answered.
func (a *GritzRPCAgent) Prompt(ctx context.Context, turn Turn) error {
	servers := make(map[string]gritzrpc.McpServer, len(a.mcpServers))
	for name, s := range a.mcpServers {
		servers[name] = gritzrpc.McpServer(s)
	}
	params, err := json.Marshal(gritzrpc.StartParams{
		ProtocolVersion: gritzrpc.ProtocolVersion,
		Resume:          turn.Resume,
		Cwd:             a.cwd,
		Task:            turn.Task,
		Events:          turn.Events,
		Links:           turn.Links,
		McpServers:      servers,
	})
	if err != nil {
		return fmt.Errorf("failed to encode run/start: %w", err)
	}
	id := startID
	start := gritzrpc.Message{JSONRPC: "2.0", ID: &id, Method: gritzrpc.MethodRunStart, Params: params}

	a.log.Info("starting gritzrpc agent", "command", a.options.Command, "args", a.options.Args,
		"resume", turn.Resume, "events", len(turn.Events))

	// exec.Command, not CommandContext: cancellation is the two-stage stop
	// below, which exec's one-shot Cancel hook cannot express.
	cmd := exec.Command(a.options.Command, a.options.Args...)
	cmd.Dir = a.cwd
	cmd.Env = a.env
	cmd.Stderr = a.log.Stderr()
	// Create a new process group so the stop reaches the agent's children.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Bounds Wait on the stderr copy when a child outlives the agent.
	cmd.WaitDelay = gritzrpcKillGrace

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	// Stdout is an os.Pipe rather than cmd.StdoutPipe, whose read end Wait
	// closes on exit: the response may still be unread in the pipe by then.
	stdout, stdoutW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer stdout.Close()
	cmd.Stdout = stdoutW
	err = cmd.Start()
	stdoutW.Close()
	if err != nil {
		return fmt.Errorf("failed to start gritzrpc agent: %w", err)
	}

	r := &gritzrpcRun{log: a.log, stdin: stdin}
	// Take the write lock before anything else can send, so run/start is the
	// first message on stdin. The goroutine writing it releases the lock.
	r.mu.Lock()
	go func() {
		defer r.mu.Unlock()
		r.write(start)
	}()

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	lineCh := make(chan []byte)
	go func() {
		defer close(lineCh)
		for line, err := range lines.Read(stdout) {
			if err != nil {
				// Closed below after the drain deadline, or a real read error;
				// drain the rest either way so the agent never blocks on a full
				// pipe.
				if !errors.Is(err, os.ErrClosed) {
					a.log.Error("failed to read gritzrpc agent output", "err", err)
				}
				_, _ = io.Copy(io.Discard, stdout)
				return
			}
			lineCh <- line
		}
	}()

	pid := cmd.Process.Pid
	var (
		timer   *time.Timer
		timerC  <-chan time.Time
		onTimer func()
	)
	after := func(d time.Duration, f func()) {
		if timer != nil {
			timer.Stop()
		}
		timer = time.NewTimer(d)
		timerC = timer.C
		onTimer = f
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	kill := func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	term := func() {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		after(gritzrpcKillGrace, kill)
	}
	// stopping is set once the process is being stopped, by its response or
	// by gritz, so the stop starts once.
	stopping := false
	stop := func() {
		if stopping {
			return
		}
		stopping = true
		go r.send(gritzrpc.Message{JSONRPC: "2.0", Method: gritzrpc.MethodRunCancel})
		after(a.cancelGrace, term)
	}

	ctxDone := ctx.Done()
	hasExited := false
	var waitErr error
	for lineCh != nil || !hasExited {
		select {
		case line, ok := <-lineCh:
			if !ok {
				lineCh = nil
				continue
			}
			responded := r.resp != nil
			r.handle(ctx, line)
			if r.fail != nil {
				stop()
			} else if !responded && r.resp != nil && !stopping {
				// The agent answered: close stdin so it knows to exit.
				stopping = true
				_ = stdin.Close()
				after(gritzrpcExitGrace, term)
			}
		case waitErr = <-exited:
			hasExited = true
			// The process is gone, so the stop is moot. Bound the drain of
			// stdout, which a child that inherited it may hold open.
			after(gritzrpcKillGrace, func() { _ = stdout.Close() })
		case <-ctxDone:
			ctxDone = nil
			if !hasExited {
				stop()
			}
		case <-timerC:
			timerC = nil
			onTimer()
		}
	}

	if ctx.Err() != nil {
		return fmt.Errorf("gritzrpc agent cancelled: %w", ctx.Err())
	}
	if r.fail != nil {
		return r.fail
	}
	if r.resp == nil {
		return fmt.Errorf("gritzrpc agent exited without responding: %s", cmd.ProcessState)
	}
	if waitErr != nil {
		a.log.Warn("gritzrpc agent exited after responding", "err", waitErr)
	}
	if rpcErr := r.resp.Error; rpcErr != nil {
		if rpcErr.Data != nil {
			a.log.Error("gritzrpc agent failed", "err", rpcErr, "data", string(rpcErr.Data))
		}
		return fmt.Errorf("gritzrpc agent: %w", rpcErr)
	}
	return nil
}

// Close releases any resources held by the agent.
func (a *GritzRPCAgent) Close() error {
	return nil
}

// gritzrpcRun is the state of one gritzrpc process.
type gritzrpcRun struct {
	log *DriverLog

	// mu serialises writes to stdin.
	mu    sync.Mutex
	stdin io.Writer

	// resp is the run/start response, once received.
	resp *gritzrpc.Message
	// fail is set by the first invalid stdout line.
	fail error
}

// send writes msg to the agent's stdin.
func (r *gritzrpcRun) send(msg gritzrpc.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.write(msg)
}

// write writes msg to the agent's stdin; r.mu must be held.
func (r *gritzrpcRun) write(msg gritzrpc.Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		panic(fmt.Sprintf("gritzrpc: marshal %s message: %v", msg.Method, err))
	}
	// A failed write means the agent closed stdin or exited; exit handling
	// reports the outcome.
	_, _ = r.stdin.Write(append(data, '\n'))
}

// handle processes one stdout line.
func (r *gritzrpcRun) handle(ctx context.Context, line []byte) {
	var msg gritzrpc.Message
	if err := json.Unmarshal(line, &msg); err != nil || msg.JSONRPC != "2.0" {
		if len(line) > gritzrpcMaxLineInError {
			line = line[:gritzrpcMaxLineInError]
		}
		if r.resp != nil || r.fail != nil {
			// The outcome is already decided; the line cannot change it.
			r.log.Warn("gritzrpc agent: invalid stdout line", "line", string(line))
			return
		}
		r.fail = fmt.Errorf("gritzrpc agent: invalid stdout line: %s", line)
		return
	}
	switch {
	case msg.Method != "" && msg.ID != nil:
		// gritz exposes no methods.
		go r.send(gritzrpc.Message{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Error: &gritzrpc.Error{
				Code:    gritzrpc.CodeMethodNotFound,
				Message: "method not found: " + msg.Method,
			},
		})
	case msg.Method == gritzrpc.MethodLog:
		r.handleLog(ctx, msg.Params)
	case msg.Method != "":
		r.log.Warn("gritzrpc agent: unknown notification", "method", msg.Method)
	case msg.ID == nil || *msg.ID != startID || r.resp != nil:
		r.log.Warn("gritzrpc agent: unexpected response", "line", string(line))
	default:
		r.resp = &msg
	}
}

// handleLog writes a log notification to the driver log.
func (r *gritzrpcRun) handleLog(ctx context.Context, params json.RawMessage) {
	var p gritzrpc.LogParams
	if err := json.Unmarshal(params, &p); err != nil {
		r.log.Warn("gritzrpc agent: invalid log params", "params", string(params), "err", err)
		return
	}
	var level slog.Level
	switch p.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	attrs := []slog.Attr{slog.String("agent", "gritzrpc")}
	for _, k := range slices.Sorted(maps.Keys(p.Attrs)) {
		if s, ok := p.Attrs[k].(string); ok {
			attrs = append(attrs, slog.String(k, s))
			continue
		}
		// Re-encoding a decoded JSON value cannot fail.
		data, _ := json.Marshal(p.Attrs[k])
		attrs = append(attrs, slog.String(k, string(data)))
	}
	r.log.LogAttrs(ctx, level, p.Msg, attrs...)
}

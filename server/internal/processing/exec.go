package processing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// maxCapture bounds how much stdout/stderr of a child process is kept.
const maxCapture = 16 << 10

// boundedBuffer keeps at most limit bytes (the tail), so a noisy tool cannot
// exhaust memory. An optional observer sees every line (progress parsing).
type boundedBuffer struct {
	mu      sync.Mutex
	limit   int
	buf     []byte
	partial []byte
	observe func(line string)
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.limit {
		b.buf = b.buf[len(b.buf)-b.limit:]
	}
	if b.observe != nil {
		b.partial = append(b.partial, p...)
		for {
			i := bytes.IndexAny(b.partial, "\r\n")
			if i < 0 {
				break
			}
			b.observe(string(b.partial[:i]))
			b.partial = b.partial[i+1:]
		}
		if len(b.partial) > b.limit {
			b.partial = b.partial[len(b.partial)-b.limit:]
		}
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// ToolError describes a failed external tool run. Its text is internal
// (it may contain paths) and never becomes a public job message.
type ToolError struct {
	Tool     string
	ExitCode int
	TimedOut bool
	Stderr   string
	Err      error
}

func (e *ToolError) Error() string {
	if e.TimedOut {
		return fmt.Sprintf("%s timed out", e.Tool)
	}
	return fmt.Sprintf("%s exited with %d: %s", e.Tool, e.ExitCode, e.Stderr)
}

func (e *ToolError) Unwrap() error { return e.Err }

type runOptions struct {
	timeout      time.Duration
	onStderrLine func(string)
}

// runTool executes bin with explicit arguments (never a shell). The process
// runs in its own process group so cancellation and timeouts terminate it and
// any children: SIGTERM first, SIGKILL after a short grace period.
func runTool(ctx context.Context, bin string, args []string, options runOptions) (stdout string, err error) {
	ctx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	out := &boundedBuffer{limit: maxCapture}
	errOut := &boundedBuffer{limit: maxCapture, observe: options.onStderrLine}
	cmd.Stdout, cmd.Stderr = out, errOut
	cmd.Stdin = nil
	configureProcessGroup(cmd)
	cmd.WaitDelay = 5 * time.Second
	runErr := cmd.Run()
	if ctx.Err() != nil && cmd.Process != nil {
		// Reap any grandchild that ignored SIGTERM.
		killProcessGroup(cmd.Process.Pid)
	}
	if runErr == nil {
		return out.String(), nil
	}
	toolErr := &ToolError{Tool: bin, ExitCode: -1, Stderr: errOut.String(), Err: runErr}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		toolErr.ExitCode = exitErr.ExitCode()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		toolErr.TimedOut = true
	}
	if parentErr := context.Cause(ctx); errors.Is(parentErr, context.Canceled) {
		toolErr.Err = errors.Join(context.Canceled, runErr)
	}
	return out.String(), toolErr
}

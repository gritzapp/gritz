// Package validatescript runs an image's warmup script on the AWS Lambda
// MicroVM /validate build hook. The hook runs on a MicroVM started from the
// newly created image, and Lambda prefetches the snapshot regions read during
// it on every later launch, so the script's job is to read what tasks will
// read. The script runs at most once; the hook answers 503 while it runs and
// its result afterwards.
package validatescript

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/gritzapp/gritz/internal/x/awsmicrovm"
)

// Options configures a Script.
type Options struct {
	// Path is the script. Required.
	Path string
	// Environ is the script's environment.
	Environ []string
	// Output receives the script's stdout and stderr. Defaults to os.Stderr.
	Output *os.File
}

// Script runs an image's validate script at most once and remembers the result.
type Script struct {
	ctx  context.Context
	opts Options

	mu       sync.Mutex
	started  bool
	finished bool
	err      error
}

// New returns a Script for opts.Path, or an error if it is not an executable
// regular file; ctx bounds the script's run.
func New(ctx context.Context, opts Options) (*Script, error) {
	if opts.Path == "" {
		panic("validatescript: Options.Path is required")
	}
	if opts.Output == nil {
		opts.Output = os.Stderr
	}
	info, err := os.Stat(opts.Path)
	if err != nil {
		return nil, fmt.Errorf("validate script: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("validate script %s: not a regular file", opts.Path)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return nil, fmt.Errorf("validate script %s: not executable", opts.Path)
	}
	return &Script{ctx: ctx, opts: opts}, nil
}

// Validate is an awsmicrovm.Handler.Validate hook: it starts the script on the
// first call and returns awsmicrovm.ErrInProgress while it runs, nil once it
// exits 0, and its error once it fails.
func (s *Script) Validate(context.Context, awsmicrovm.ValidateHookRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.finished:
		return s.err
	case s.started:
		return awsmicrovm.ErrInProgress
	}
	s.started = true
	// The script outlives the request that starts it, so it runs under the
	// context New was given, not the request's.
	c := exec.CommandContext(s.ctx, s.opts.Path)
	c.Env = s.opts.Environ
	// A nil Stdin reads from /dev/null. Output is an *os.File, so the child
	// writes to it directly and Wait has no pipe to drain.
	c.Stdout = s.opts.Output
	c.Stderr = s.opts.Output
	if err := c.Start(); err != nil {
		s.finished = true
		s.err = fmt.Errorf("validate script %s: %w", s.opts.Path, err)
		return s.err
	}
	go s.wait(c)
	return awsmicrovm.ErrInProgress
}

// wait records the script's result once it exits.
func (s *Script) wait(c *exec.Cmd) {
	err := c.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = true
	if err != nil {
		s.err = fmt.Errorf("validate script %s: %w", s.opts.Path, err)
	}
}

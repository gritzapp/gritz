package agent

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"
)

// ProcessAgent implements Agent by running an arbitrary command.
//
// The prompt is written to the command's stdin. GRITZ_RESUME is set to "true"
// when the run continues a previous session. Stdout and stderr are teed raw
// into the driver log. With no command configured, Prompt returns immediately.
type ProcessAgent struct {
	log     *DriverLog
	cwd     string
	env     []string
	options *ProcessOptions
}

// Prompt runs the configured command with the prompt on stdin and waits for it to exit.
func (a *ProcessAgent) Prompt(ctx context.Context, prompt string, resume bool) error {
	if a.options == nil || len(a.options.Command) == 0 {
		a.log.Info("process agent has no command, exiting")
		return nil
	}
	a.log.Info("sending prompt", "text", prompt, "command", a.options.Command)

	env := a.env
	if resume {
		env = append(slices.Clip(env), "GRITZ_RESUME=true")
	}

	cmd := exec.CommandContext(ctx, a.options.Command[0], a.options.Command[1:]...)
	cmd.Dir = a.cwd
	cmd.Env = env
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stdout = a.log.Stdout()
	cmd.Stderr = a.log.Stderr()

	// Create a new process group so we can kill all child processes.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second

	return cmd.Run()
}

// Close releases any resources held by the agent.
func (a *ProcessAgent) Close() error {
	return nil
}

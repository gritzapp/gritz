// Package puppet is an agent with no behaviour of its own. The Agent forwards
// each Prompt call to a Server, one HTTP request held open until the Server's
// owner (the e2e harness) ends the Run it became, so a test decides how every
// run ends.
package puppet

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

// Options configures an Agent.
type Options struct {
	// URL is the Server endpoint each Prompt call is forwarded to.
	URL string
	// Log is where the agent logs. It defaults to discarding.
	Log *slog.Logger `json:"-"`
}

// Request is the body of a forwarded Prompt call.
type Request struct {
	TaskID int64  `json:"task_id"`
	Prompt string `json:"prompt"`
	Resume bool   `json:"resume"`
}

// Response is the Server's answer; Error empty means success.
type Response struct {
	Error string `json:"error,omitempty"`
}

// Agent forwards each Prompt call to a Server and returns its answer.
// Cancelling the Prompt's ctx aborts the request.
type Agent struct {
	log    *slog.Logger
	taskID int64
	url    string
}

// New returns an Agent that forwards the given task's Prompt calls to opts.URL.
func New(taskID int64, opts Options) *Agent {
	return &Agent{
		log:    cmp.Or(opts.Log, slog.New(slog.DiscardHandler)),
		taskID: taskID,
		url:    opts.URL,
	}
}

// Prompt posts a Request to the Server and waits for its Response.
func (a *Agent) Prompt(ctx context.Context, prompt string, resume bool) error {
	a.log.Info("puppet agent forwarding prompt", "url", a.url, "resume", resume)
	body, err := json.Marshal(Request{TaskID: a.taskID, Prompt: prompt, Resume: resume})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("puppet request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("puppet request: unexpected status %s", resp.Status)
	}
	var answer Response
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return fmt.Errorf("puppet response: %w", err)
	}
	if answer.Error != "" {
		a.log.Info("puppet agent returning error", "err", answer.Error)
		return errors.New(answer.Error)
	}
	return nil
}

// Close does nothing and returns nil.
func (a *Agent) Close() error {
	return nil
}

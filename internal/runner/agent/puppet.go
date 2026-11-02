package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// PuppetAgent forwards each Prompt call to a control endpoint, one HTTP request
// held open until the endpoint answers, and returns its answer. Cancelling ctx
// aborts the request. It has no behaviour of its own: the e2e harness answers
// each request and so decides how every run ends.
type PuppetAgent struct {
	log    *DriverLog
	taskID int64
	url    string
}

// PuppetRequest is the body of a forwarded Prompt call.
type PuppetRequest struct {
	TaskID int64  `json:"task_id"`
	Prompt string `json:"prompt"`
	Resume bool   `json:"resume"`
}

// PuppetResponse is the endpoint's answer; Error empty means success.
type PuppetResponse struct {
	Error string `json:"error,omitempty"`
}

// newPuppetAgent creates a PuppetAgent for the task named by GRITZ_TASK_ID in env.
func newPuppetAgent(log *DriverLog, env []string, options *PuppetOptions) (*PuppetAgent, error) {
	if options == nil || options.URL == "" {
		return nil, errors.New("puppet agent: url is required")
	}
	raw, ok := lookupEnv(env, "GRITZ_TASK_ID")
	if !ok {
		return nil, errors.New("puppet agent: GRITZ_TASK_ID is not set")
	}
	taskID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("puppet agent: invalid GRITZ_TASK_ID: %w", err)
	}
	return &PuppetAgent{log: log, taskID: taskID, url: options.URL}, nil
}

// Prompt posts a PuppetRequest to the endpoint and waits for its PuppetResponse.
func (a *PuppetAgent) Prompt(ctx context.Context, prompt string, resume bool) error {
	a.log.Info("puppet agent forwarding prompt", "url", a.url, "resume", resume)
	body, err := json.Marshal(PuppetRequest{TaskID: a.taskID, Prompt: prompt, Resume: resume})
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
	var answer PuppetResponse
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
func (a *PuppetAgent) Close() error {
	return nil
}

// lookupEnv returns the value of the last entry for key in env, as exec does.
func lookupEnv(env []string, key string) (string, bool) {
	for _, kv := range slices.Backward(env) {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

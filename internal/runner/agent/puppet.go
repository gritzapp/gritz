package agent

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/gritzapp/gritz/internal/runner/agent/puppet"
)

// newPuppetAgent creates a puppet agent for the task named by GRITZ_TASK_ID in env.
func newPuppetAgent(log *DriverLog, env []string, options *puppet.Options) (Agent, error) {
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
	opts := *options
	opts.Log = &log.Logger
	return puppet.New(taskID, opts), nil
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

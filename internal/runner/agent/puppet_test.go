package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"gotest.tools/v3/assert"
)

// newPuppet starts an httptest endpoint served by handler and returns a puppet
// agent for task 42 pointed at it.
func newPuppet(t *testing.T, handler http.HandlerFunc) Agent {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	a, err := NewAgent(Options{
		Type:   TypePuppet,
		Env:    []string{"GRITZ_TASK_ID=42"},
		Puppet: &PuppetOptions{URL: srv.URL},
	})
	assert.NilError(t, err)
	return a
}

// answer replies to a forwarded Prompt with resp.
func answer(t *testing.T, w http.ResponseWriter, resp PuppetResponse) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	assert.Check(t, json.NewEncoder(w).Encode(resp))
}

func TestPuppetAgent_Success(t *testing.T) {
	t.Parallel()
	// Arrange - an endpoint that records the request and answers success
	var got PuppetRequest
	a := newPuppet(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Check(t, json.NewDecoder(r.Body).Decode(&got))
		answer(t, w, PuppetResponse{})
	})

	// Act
	err := a.Prompt(t.Context(), "do the thing", true)

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, got, PuppetRequest{TaskID: 42, Prompt: "do the thing", Resume: true})
}

func TestPuppetAgent_Error(t *testing.T) {
	t.Parallel()
	// Arrange - an endpoint that answers with an error
	a := newPuppet(t, func(w http.ResponseWriter, r *http.Request) {
		answer(t, w, PuppetResponse{Error: "run failed on purpose"})
	})

	// Act
	err := a.Prompt(t.Context(), "do the thing", false)

	// Assert
	assert.Error(t, err, "run failed on purpose")
}

func TestPuppetAgent_Cancel(t *testing.T) {
	t.Parallel()
	// Arrange - an endpoint that holds the request open until the client goes away
	received := make(chan struct{})
	aborted := make(chan struct{})
	a := newPuppet(t, func(w http.ResponseWriter, r *http.Request) {
		// The server only watches for the client going away once the body is read.
		_, err := io.Copy(io.Discard, r.Body)
		assert.Check(t, err)
		close(received)
		<-r.Context().Done()
		close(aborted)
	})
	ctx, cancel := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() { errc <- a.Prompt(ctx, "do the thing", false) }()
	<-received

	// Act
	cancel()

	// Assert
	err := <-errc
	assert.Assert(t, errors.Is(err, context.Canceled), "got %v", err)
	<-aborted
}

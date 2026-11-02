package puppet

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"sync"
)

// errClosed is the answer every open and later run gets once the Server is closed.
const errClosed = "puppet server closed"

// Server is the http.Handler Agents forward to. Each request becomes a Run,
// queued per task in arrival order and held open until it ends.
type Server struct {
	mu     sync.Mutex
	tasks  map[int64]*queue
	closed bool
}

// queue is one task's runs.
type queue struct {
	runs []*Run
	// next is the index of the run the next Next call returns.
	next int
	// arrived is closed, and replaced, whenever a run is appended.
	arrived chan struct{}
}

// NewServer returns a Server with no runs.
func NewServer() *Server {
	return &Server{tasks: map[int64]*queue{}}
}

// queue returns the task's queue, creating it. s.mu must be held.
func (s *Server) queue(taskID int64) *queue {
	q, ok := s.tasks[taskID]
	if !ok {
		q = &queue{arrived: make(chan struct{})}
		s.tasks[taskID] = q
	}
	return q
}

// ServeHTTP turns a forwarded Prompt call into a Run and holds the request open
// until the Run is answered or the request's context ends.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Read the whole body: the server only watches for the client going away
	// once it has been consumed.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	run := &Run{
		TaskID:    req.TaskID,
		Prompt:    req.Prompt,
		Resume:    req.Resume,
		answer:    make(chan Response, 1),
		cancelled: make(chan struct{}),
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		writeResponse(w, Response{Error: errClosed})
		return
	}
	q := s.queue(req.TaskID)
	q.runs = append(q.runs, run)
	close(q.arrived)
	q.arrived = make(chan struct{})
	s.mu.Unlock()

	select {
	case resp := <-run.answer:
		writeResponse(w, resp)
	case <-r.Context().Done():
		run.cancel()
	}
}

func writeResponse(w http.ResponseWriter, resp Response) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// Next returns the task's next run, the first one no earlier Next returned,
// waiting for it to arrive.
func (s *Server) Next(ctx context.Context, taskID int64) (*Run, error) {
	for {
		s.mu.Lock()
		q := s.queue(taskID)
		if q.next < len(q.runs) {
			run := q.runs[q.next]
			q.next++
			s.mu.Unlock()
			return run, nil
		}
		arrived := q.arrived
		s.mu.Unlock()
		select {
		case <-arrived:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Runs returns every run the task has had so far, in arrival order.
func (s *Server) Runs(taskID int64) []*Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.tasks[taskID]
	if !ok {
		return nil
	}
	return slices.Clone(q.runs)
}

// Close fails every open run, and every run that arrives later, so the Agents
// waiting on them return.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for _, q := range s.tasks {
		for _, run := range q.runs {
			_ = run.Fail(errClosed)
		}
	}
}

// Run is one forwarded Prompt call.
type Run struct {
	TaskID int64
	Prompt string
	Resume bool

	mu        sync.Mutex
	ended     bool
	answer    chan Response
	cancelled chan struct{}
}

// Complete ends the run: Prompt returns nil. It errors if the run already ended.
func (r *Run) Complete() error {
	return r.end(Response{})
}

// Fail ends the run: Prompt returns an error with msg. It errors if the run
// already ended.
func (r *Run) Fail(msg string) error {
	return r.end(Response{Error: msg})
}

// Cancelled is closed when the request ends without an answer: the Prompt's
// ctx was cancelled or the driver was killed.
func (r *Run) Cancelled() <-chan struct{} {
	return r.cancelled
}

func (r *Run) end(resp Response) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return errors.New("puppet run already ended")
	}
	r.ended = true
	r.answer <- resp
	return nil
}

// cancel ends the run because its request ended, unless it was already answered.
func (r *Run) cancel() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return
	}
	r.ended = true
	close(r.cancelled)
}

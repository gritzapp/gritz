package testrunner

import (
	"cmp"
	"context"
	"strconv"
	"sync"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
)

// DefaultServerPageSize is the page size of a Server whose ServerOptions
// leave it 0.
const DefaultServerPageSize = 100

// ServerOptions configures a Server.
type ServerOptions struct {
	// PageSize is the most events a ListRunnerEvents page holds; 0 means
	// DefaultServerPageSize.
	PageSize int
}

// Server is an in-memory stand-in for the server's runner event log: tests
// append events to it and read them back through Client. It serves
// ListRunnerEvents in pages, with the offset of the next page as the page
// token.
type Server struct {
	pageSize int

	mu     sync.Mutex
	events []*gritzv1.Event
}

// NewServer returns a Server with an empty event log.
func NewServer(opts ServerOptions) *Server {
	return &Server{pageSize: cmp.Or(opts.PageSize, DefaultServerPageSize)}
}

// Add appends events to the log.
func (s *Server) Add(events ...*gritzv1.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, events...)
}

// ListRunnerEvents serves the page of the log that starts at the request's
// page token. It ignores the request's runner: the log is the runner's.
func (s *Server) ListRunnerEvents(_ context.Context, req *gritzv1.ListRunnerEventsRequest) (*gritzv1.ListRunnerEventsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	offset := 0
	if req.GetPageToken() != "" {
		var err error
		offset, err = strconv.Atoi(req.GetPageToken())
		if err != nil {
			return nil, err
		}
	}
	end := min(offset+s.pageSize, len(s.events))
	return &gritzv1.ListRunnerEventsResponse{
		Events:        s.events[offset:end],
		NextPageToken: strconv.Itoa(end),
		More:          end < len(s.events),
	}, nil
}

// Client returns a client whose ListRunnerEvents is s's. Tests can stub its
// other RPCs, and inspect its calls.
func (s *Server) Client() *gritzclient.ClientMock {
	return &gritzclient.ClientMock{ListRunnerEventsFunc: s.ListRunnerEvents}
}

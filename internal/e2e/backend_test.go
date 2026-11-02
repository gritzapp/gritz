package e2e_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gritzapp/gritz/internal/e2e"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/backend/local"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestBackendMock(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	// No task has this sandbox, so the real backend reports it gone.
	handle := backend.Handle{Type: local.HandleType, ID: "1"}
	m1 := &backend.BackendMock{OpenFunc: func(backend.Handle) (backend.Sandbox, error) {
		return nil, errors.New("e2e: first")
	}}
	h.Backend.Mock(m1)

	_, err := h.Backend.Open(handle)
	assert.ErrorContains(t, err, "e2e: first")
	calls := m1.OpenCalls()
	assert.Assert(t, cmp.Len(calls, 1))
	assert.DeepEqual(t, calls[0].H, handle)

	m2 := &backend.BackendMock{OpenFunc: func(backend.Handle) (backend.Sandbox, error) {
		return nil, errors.New("e2e: second")
	}}
	h.Backend.Mock(m2)

	_, err = h.Backend.Open(handle)
	assert.ErrorContains(t, err, "e2e: second")
	assert.Assert(t, cmp.Len(m1.OpenCalls(), 1))
	assert.Assert(t, cmp.Len(m2.OpenCalls(), 1))

	h.Backend.Mock(nil)

	sb, err := h.Backend.Open(handle)
	assert.NilError(t, err)
	got, err := sb.Status(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, got, backend.StatusGone)
	assert.Assert(t, cmp.Len(m2.OpenCalls(), 1))
}

func TestBackendMockSandbox(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	// No task has this sandbox, so the real sandbox reports gone.
	sb, err := h.Backend.Open(backend.Handle{Type: local.HandleType, ID: "1"})
	assert.NilError(t, err)
	m1 := &backend.SandboxMock{StatusFunc: func(context.Context) (backend.Status, error) {
		return backend.StatusUp, nil
	}}
	// Installed after the sandbox was opened, and for its task only.
	h.Backend.MockSandbox(1, m1)
	h.Backend.MockSandbox(2, &backend.SandboxMock{StatusFunc: func(context.Context) (backend.Status, error) {
		return 0, errors.New("e2e: other task")
	}})

	got, err := sb.Status(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, got, backend.StatusUp)
	assert.Assert(t, cmp.Len(m1.StatusCalls(), 1))

	m2 := &backend.SandboxMock{StatusFunc: func(context.Context) (backend.Status, error) {
		return backend.StatusSuspended, nil
	}}
	h.Backend.MockSandbox(1, m2)

	got, err = sb.Status(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, got, backend.StatusSuspended)
	assert.Assert(t, cmp.Len(m1.StatusCalls(), 1))
	assert.Assert(t, cmp.Len(m2.StatusCalls(), 1))

	h.Backend.MockSandbox(1, nil)

	got, err = sb.Status(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, got, backend.StatusGone)
	assert.Assert(t, cmp.Len(m2.StatusCalls(), 1))
}

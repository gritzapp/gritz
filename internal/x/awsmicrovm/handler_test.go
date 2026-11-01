package awsmicrovm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func post(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHandlerRoutesAndDispatches(t *testing.T) {
	var ranPayload, ranID, terminatedID string
	h := &Handler{
		Run: func(_ context.Context, req RunHookRequest) error {
			ranPayload = req.Payload
			ranID = req.MicrovmID
			return nil
		},
		Terminate: func(_ context.Context, req TerminateHookRequest) error {
			terminatedID = req.MicrovmID
			return nil
		},
	}

	assert.Equal(t, post(t, h, HookRun, `{"microvmId":"mvm-1","runHookPayload":"opaque"}`).Code, http.StatusOK)
	assert.Equal(t, ranPayload, "opaque")
	assert.Equal(t, ranID, "mvm-1")

	assert.Equal(t, post(t, h, HookTerminate, `{"microvmId":"mvm-1"}`).Code, http.StatusOK)
	assert.Equal(t, terminatedID, "mvm-1")
}

func TestHandlerRoutesBuildHooks(t *testing.T) {
	var ready, validate int
	h := &Handler{
		Ready:    func(context.Context, ReadyHookRequest) error { ready++; return nil },
		Validate: func(context.Context, ValidateHookRequest) error { validate++; return nil },
	}

	assert.Equal(t, post(t, h, HookReady, "").Code, http.StatusOK)
	assert.Equal(t, ready, 1)

	assert.Equal(t, post(t, h, HookValidate, "").Code, http.StatusOK)
	assert.Equal(t, validate, 1)
}

func TestHandlerNilFieldsDefaultTo200(t *testing.T) {
	h := &Handler{} // no hooks set
	for _, path := range []string{
		HookRun,
		HookTerminate,
		HookSuspend,
		HookResume,
		HookReady,
		HookValidate,
	} {
		assert.Equal(t, post(t, h, path, "").Code, http.StatusOK, "path %s with nil hook", path)
	}
}

func TestHandlerHookErrorIs500(t *testing.T) {
	h := &Handler{Run: func(context.Context, RunHookRequest) error { return context.Canceled }}
	assert.Equal(t, post(t, h, HookRun, `{"microvmId":"x"}`).Code, http.StatusInternalServerError)
}

func TestHandlerHookInProgressIs503(t *testing.T) {
	h := &Handler{Validate: func(context.Context, ValidateHookRequest) error { return ErrInProgress }}
	assert.Equal(t, post(t, h, HookValidate, "").Code, http.StatusServiceUnavailable)
}

func TestHandlerBadBodyIs400(t *testing.T) {
	h := &Handler{Run: func(context.Context, RunHookRequest) error { return nil }}
	assert.Equal(t, post(t, h, HookRun, `{not json`).Code, http.StatusBadRequest)
}

func TestHandlerUnknownPathIs404(t *testing.T) {
	h := &Handler{}
	assert.Equal(t, post(t, h, "/aws/lambda-microvms/runtime/v1/bogus", "").Code, http.StatusNotFound)
}

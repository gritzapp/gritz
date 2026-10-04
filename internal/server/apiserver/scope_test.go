package apiserver

import (
	"reflect"
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/proto/gritz/v1/gritzv1connect"
	"gotest.tools/v3/assert"
)

// TestAllMethodsScopeChecked is the completeness safety net for the explicit
// per-handler enforcement model (proposal §8). There is no central default-deny
// interceptor, so a newly added RPC that forgets its top-of-handler Allow would
// ship fail-open. This test enumerates every method of GritzServiceHandler by
// reflection (so the list can't drift from the proto) and asserts that a caller
// with empty scopes is denied with PermissionDenied — except the small, audited
// exempt set, which is skipped. Every non-exempt handler denies at its scope
// gate before touching the store, so no database is needed.
func TestAllMethodsScopeChecked(t *testing.T) {
	t.Parallel()
	// scopeExemptMethods are the only methods that do NOT gate on the caller's
	// scopes, so they are skipped. Each entry is justified; every other method
	// must deny an empty-scopes caller, so adding a new RPC forces either a real
	// scope check or a deliberate, reviewed entry here.
	scopeExemptMethods := map[string]string{
		// Ping carries no caller data and returns the server version unconditionally.
		"Ping": "no caller data; returns version unconditionally",
		// GetProfile acts on the caller's own identity (the identity axis, not an
		// intra-org capability); it is gated by RequireUserInterceptor, not scopes.
		"GetProfile": "identity axis; gated by being an authenticated user, not scopes",
		// CreateGitHubToken is intentionally Unimplemented on the API server; only
		// the runner proxy serves it (#806).
		"CreateGitHubToken": "API server leaves it Unimplemented; served by the runner proxy",
	}
	srv := New(Options{})
	// A present-but-scopeless caller: tenancy is satisfied (a caller exists) so
	// every handler reaches its scope gate, which must deny.
	ctx := apiauth.WithUser(t.Context(), &apiauth.UserInfo{})

	handlerType := reflect.TypeOf((*gritzv1connect.GritzServiceHandler)(nil)).Elem()
	srvVal := reflect.ValueOf(srv)
	for i := 0; i < handlerType.NumMethod(); i++ {
		name := handlerType.Method(i).Name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if reason, exempt := scopeExemptMethods[name]; exempt {
				t.Skipf("scope-exempt: %s", reason)
			}
			method := srvVal.MethodByName(name)
			// Interface method signature: func(context.Context, *Req) (*Resp, error).
			// The bound method on srv drops the receiver, so In(1) is the request.
			reqType := method.Type().In(1)
			req := reflect.New(reqType.Elem())
			out := method.Call([]reflect.Value{reflect.ValueOf(ctx), req})
			err, _ := out[1].Interface().(error)
			assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied,
				"%s must deny an empty-scopes caller", name)
		})
	}
}

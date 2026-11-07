package customserver

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/server/eventrouter"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// problemType is the custom event type the handler tests submit events of.
var problemType = model.EventTypeDef{
	Source: eventrouter.SourceCustom,
	Type:   "problem",
	Label:  "Dynatrace Problem",
	Attrs: []model.AttrDef{
		{Key: "body"},
		{Key: "url"},
		{Key: "severity"},
		{Key: "tag"},
	},
}

// newRequest returns a POST of body to the webhook, authenticated as caller
// when it's non-nil.
func newRequest(caller *apiauth.UserInfo, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/webhook/custom", strings.NewReader(body))
	if caller != nil {
		req = req.WithContext(apiauth.WithUser(req.Context(), caller))
	}
	return req
}

// keyCaller is an API-key caller in org 7 holding every scope.
var keyCaller = &apiauth.UserInfo{ID: "user-1", OrgID: 7, Type: apiauth.AuthTypeKey, Scopes: authscope.Admin()}

func TestWebhookHandler(t *testing.T) {
	// Arrange
	router := &RouterMock{
		RouteFunc: func(ctx context.Context, input eventrouter.InputEvent) (int, error) {
			return 2, nil
		},
	}
	store := &StoreMock{
		GetOrgCustomEventTypesFunc: func(ctx context.Context, tx *sql.Tx, orgID int64) ([]model.EventTypeDef, error) {
			return []model.EventTypeDef{problemType}, nil
		},
	}
	h := &WebhookHandler{Router: router, Store: store}
	req := newRequest(keyCaller, `{
		"type": "problem",
		"url": "https://abc123.live.dynatrace.com/#problems/problemdetails;pid=-123_456V2",
		"body": "CPU saturation on host web-1",
		"attrs": {"severity": ["RESOURCE_CONTENTION"], "tag": ["team:payments", "env:prod"]},
		"details": {"impact": "INFRASTRUCTURE", "state": "OPEN"}
	}`)
	rec := httptest.NewRecorder()

	// Act
	h.ServeHTTP(rec, req)

	// Assert
	assert.Equal(t, rec.Code, http.StatusOK)
	assert.Equal(t, rec.Header().Get("Content-Type"), "application/json")
	assert.Equal(t, rec.Body.String(), "{\"routed\":2}\n")
	assert.Equal(t, len(store.GetOrgCustomEventTypesCalls()), 1)
	assert.Equal(t, store.GetOrgCustomEventTypesCalls()[0].OrgID, int64(7))
	// The event has no actor: UserID and Meta stay empty, and it belongs to
	// the key's org only.
	assert.DeepEqual(t, router.RoutedInputs(), []eventrouter.InputEvent{{
		Source:      "custom",
		Type:        "problem",
		Description: "Dynatrace Problem",
		Data:        "CPU saturation on host web-1",
		URL:         "https://abc123.live.dynatrace.com/#problems/problemdetails;pid=-123_456V2",
		Orgs:        []int64{7},
		Attrs: eventrouter.Attrs{
			"severity": {"RESOURCE_CONTENTION"},
			"tag":      {"team:payments", "env:prod"},
		},
		Details: map[string]string{"impact": "INFRASTRUCTURE", "state": "OPEN"},
	}})
}

func TestWebhookHandler_NoCaller(t *testing.T) {
	// Arrange
	h := &WebhookHandler{}
	req := newRequest(nil, `{}`)
	rec := httptest.NewRecorder()

	// Act
	h.ServeHTTP(rec, req)

	// Assert
	assert.Equal(t, rec.Code, http.StatusUnauthorized)
}

func TestWebhookHandler_CookieCaller(t *testing.T) {
	// Arrange
	h := &WebhookHandler{}
	caller := &apiauth.UserInfo{ID: "user-1", OrgID: 7, Type: apiauth.AuthTypeCookie, Scopes: authscope.Admin()}
	req := newRequest(caller, `{}`)
	rec := httptest.NewRecorder()

	// Act
	h.ServeHTTP(rec, req)

	// Assert
	assert.Equal(t, rec.Code, http.StatusUnauthorized)
	assert.Assert(t, cmp.Contains(rec.Body.String(), "an API key is required"))
}

func TestWebhookHandler_MissingScope(t *testing.T) {
	// Arrange
	h := &WebhookHandler{}
	caller := &apiauth.UserInfo{
		ID:     "user-1",
		OrgID:  7,
		Type:   apiauth.AuthTypeKey,
		Scopes: authscope.Scopes{authscope.New(authscope.OpOrgRead)},
	}
	req := newRequest(caller, `{}`)
	rec := httptest.NewRecorder()

	// Act
	h.ServeHTTP(rec, req)

	// Assert
	assert.Equal(t, rec.Code, http.StatusForbidden)
}

func TestWebhookHandler_BadRequest(t *testing.T) {
	tests := []struct {
		name string
		body string
		err  string
	}{
		{
			name: "MalformedJSON",
			body: `{"type":`,
			err:  "invalid envelope",
		},
		{
			name: "UnknownField",
			body: `{"type": "problem", "url": "https://example.com/p/1", "description": "x"}`,
			err:  `unknown field "description"`,
		},
		{
			name: "TooLarge",
			body: `{"type": "problem", "url": "https://example.com/p/1", "body": "` + strings.Repeat("x", maxEnvelopeBytes) + `"}`,
			err:  "request body too large",
		},
		{
			name: "UnknownType",
			body: `{"type": "deploy", "url": "https://example.com/p/1"}`,
			err:  `unknown event type "deploy"`,
		},
		{
			name: "EmptyURL",
			body: `{"type": "problem"}`,
			err:  "url is required",
		},
		{
			name: "RelativeURL",
			body: `{"type": "problem", "url": "/p/1"}`,
			err:  `url "/p/1" must be an absolute http or https URL`,
		},
		{
			name: "NonHTTPURL",
			body: `{"type": "problem", "url": "ftp://example.com/p/1"}`,
			err:  `url "ftp://example.com/p/1" must be an absolute http or https URL`,
		},
		{
			name: "URLWithoutHost",
			body: `{"type": "problem", "url": "https:p/1"}`,
			err:  `url "https:p/1" must be an absolute http or https URL`,
		},
		{
			name: "UnparsableURL",
			body: `{"type": "problem", "url": "https://exa mple.com/%zz"}`,
			err:  `must be an absolute http or https URL`,
		},
		{
			name: "BodyAttr",
			body: `{"type": "problem", "url": "https://example.com/p/1", "attrs": {"body": ["x"]}}`,
			err:  `attr "body" must be sent as the envelope's "body" field`,
		},
		{
			name: "URLAttr",
			body: `{"type": "problem", "url": "https://example.com/p/1", "attrs": {"url": ["https://example.com/p/2"]}}`,
			err:  `attr "url" must be sent as the envelope's "url" field`,
		},
		{
			name: "UndeclaredAttr",
			body: `{"type": "problem", "url": "https://example.com/p/1", "attrs": {"severity": ["high"], "region": ["eu"]}}`,
			err:  `attr "region" not declared by event type "problem"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			router := &RouterMock{}
			h := &WebhookHandler{
				Router: router,
				Store: &StoreMock{
					GetOrgCustomEventTypesFunc: func(ctx context.Context, tx *sql.Tx, orgID int64) ([]model.EventTypeDef, error) {
						return []model.EventTypeDef{problemType}, nil
					},
				},
			}
			req := newRequest(keyCaller, tt.body)
			rec := httptest.NewRecorder()

			// Act
			h.ServeHTTP(rec, req)

			// Assert
			assert.Equal(t, rec.Code, http.StatusBadRequest)
			assert.Assert(t, cmp.Contains(rec.Body.String(), tt.err))
			assert.Equal(t, len(router.RouteCalls()), 0)
		})
	}
}

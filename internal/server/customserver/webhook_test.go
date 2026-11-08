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

func TestWebhookHandler(t *testing.T) {
	// Arrange
	router := &RouterMock{
		RouteFunc: func(ctx context.Context, input eventrouter.InputEvent) (int, error) {
			return 2, nil
		},
	}
	store := &StoreMock{
		GetOrgCustomEventTypesFunc: func(ctx context.Context, tx *sql.Tx, orgID int64) ([]model.EventTypeDef, error) {
			return []model.EventTypeDef{{
				Source: eventrouter.SourceCustom,
				Type:   "problem",
				Label:  "Dynatrace Problem",
				Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity"}, {Key: "tag"}},
			}}, nil
		},
	}
	h := &WebhookHandler{Router: router, Store: store}
	req, err := NewWebhookRequest(t.Context(), "/webhook/custom", "xat_test", Envelope{
		Type:    "problem",
		URL:     "https://abc123.live.dynatrace.com/#problems/problemdetails;pid=-123_456V2",
		Body:    "CPU saturation on host web-1",
		Attrs:   map[string][]string{"severity": {"RESOURCE_CONTENTION"}, "tag": {"team:payments", "env:prod"}},
		Details: map[string]string{"impact": "INFRASTRUCTURE", "state": "OPEN"},
	})
	assert.NilError(t, err)
	req = req.WithContext(apiauth.WithUser(req.Context(), &apiauth.UserInfo{
		ID:     "user-1",
		OrgID:  7,
		Type:   apiauth.AuthTypeKey,
		Scopes: authscope.Admin(),
	}))
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
		Description: "custom event",
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

func TestWebhookHandler_MissingScope(t *testing.T) {
	// Arrange
	h := &WebhookHandler{}
	req, err := NewWebhookRequest(t.Context(), "/webhook/custom", "xat_test", Envelope{
		Type: "problem",
		URL:  "https://example.com/p/1",
	})
	assert.NilError(t, err)
	req = req.WithContext(apiauth.WithUser(req.Context(), &apiauth.UserInfo{
		ID:     "user-1",
		OrgID:  7,
		Type:   apiauth.AuthTypeKey,
		Scopes: authscope.Scopes{authscope.New(authscope.OpOrgRead)},
	}))
	rec := httptest.NewRecorder()

	// Act
	h.ServeHTTP(rec, req)

	// Assert
	assert.Equal(t, rec.Code, http.StatusForbidden)
}

func TestWebhookHandler_MalformedBody(t *testing.T) {
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
			body: `{"type": "problem", "url": "https://example.com/p/1", "summary": "x"}`,
			err:  `unknown field "summary"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			router := &RouterMock{}
			h := &WebhookHandler{Router: router}
			req := httptest.NewRequest(http.MethodPost, "/webhook/custom", strings.NewReader(tt.body))
			req = req.WithContext(apiauth.WithUser(req.Context(), &apiauth.UserInfo{
				ID:     "user-1",
				OrgID:  7,
				Type:   apiauth.AuthTypeKey,
				Scopes: authscope.Admin(),
			}))
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

func TestWebhookHandler_InvalidEnvelope(t *testing.T) {
	tests := []struct {
		name string
		env  Envelope
		err  string
	}{
		{
			name: "TooLarge",
			env:  Envelope{Type: "problem", URL: "https://example.com/p/1", Body: strings.Repeat("x", maxEnvelopeBytes)},
			err:  "request body too large",
		},
		{
			name: "UnknownType",
			env:  Envelope{Type: "deploy", URL: "https://example.com/p/1"},
			err:  `unknown event type "deploy"`,
		},
		{
			name: "EmptyURL",
			env:  Envelope{Type: "problem"},
			err:  "url is required",
		},
		{
			name: "RelativeURL",
			env:  Envelope{Type: "problem", URL: "/p/1"},
			err:  `url "/p/1" must be an absolute http or https URL`,
		},
		{
			name: "NonHTTPURL",
			env:  Envelope{Type: "problem", URL: "ftp://example.com/p/1"},
			err:  `url "ftp://example.com/p/1" must be an absolute http or https URL`,
		},
		{
			name: "URLWithoutHost",
			env:  Envelope{Type: "problem", URL: "https:p/1"},
			err:  `url "https:p/1" must be an absolute http or https URL`,
		},
		{
			name: "UnparsableURL",
			env:  Envelope{Type: "problem", URL: "https://exa mple.com/%zz"},
			err:  `must be an absolute http or https URL`,
		},
		{
			name: "BodyAttr",
			env:  Envelope{Type: "problem", URL: "https://example.com/p/1", Attrs: map[string][]string{"body": {"x"}}},
			err:  `attr "body" must be sent as the envelope's "body" field`,
		},
		{
			name: "URLAttr",
			env:  Envelope{Type: "problem", URL: "https://example.com/p/1", Attrs: map[string][]string{"url": {"https://example.com/p/2"}}},
			err:  `attr "url" must be sent as the envelope's "url" field`,
		},
		{
			name: "UndeclaredAttr",
			env:  Envelope{Type: "problem", URL: "https://example.com/p/1", Attrs: map[string][]string{"severity": {"high"}, "region": {"eu"}}},
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
						return []model.EventTypeDef{{
							Source: eventrouter.SourceCustom,
							Type:   "problem",
							Label:  "Dynatrace Problem",
							Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity"}},
						}}, nil
					},
				},
			}
			req, err := NewWebhookRequest(t.Context(), "/webhook/custom", "xat_test", tt.env)
			assert.NilError(t, err)
			req = req.WithContext(apiauth.WithUser(req.Context(), &apiauth.UserInfo{
				ID:     "user-1",
				OrgID:  7,
				Type:   apiauth.AuthTypeKey,
				Scopes: authscope.Admin(),
			}))
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

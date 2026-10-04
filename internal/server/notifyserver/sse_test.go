package notifyserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/server/pubsub"
	"github.com/gritzapp/gritz/internal/x/sse"
	"gotest.tools/v3/assert"
)

// allowOrgResolver returns a mock that allows the given user to subscribe
// to any of the configured orgs and rejects everything else.
func allowOrgResolver(userID string, allow ...int64) *OrgResolverMock {
	allowed := make(map[int64]bool, len(allow))
	for _, id := range allow {
		allowed[id] = true
	}
	return &OrgResolverMock{
		ResolveOrgFunc: func(ctx context.Context, callerID string, orgID int64) (int64, error) {
			if callerID != userID || !allowed[orgID] {
				return 0, fmt.Errorf("user %s not a member of org %d", callerID, orgID)
			}
			return orgID, nil
		},
	}
}

func TestSSE_Unauthenticated(t *testing.T) {
	t.Parallel()

	ps := pubsub.NewLocalPubSub()
	srv := New(Options{Subscriber: ps})

	// No user in context — bare handler without WithTestUser.
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/events")
	assert.NilError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, resp.StatusCode, http.StatusUnauthorized)
}

func TestSSE(t *testing.T) {
	t.Parallel()

	ps := pubsub.NewLocalPubSub()
	const orgID int64 = 1
	srv := New(Options{Subscriber: ps})

	ts := httptest.NewServer(apiauth.WithTestUser(srv.Handler(), &apiauth.UserInfo{ID: "u", OrgID: orgID, Type: apiauth.AuthTypeApp}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/events", nil)
	assert.NilError(t, err)

	resp, err := http.DefaultClient.Do(req)
	assert.NilError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, resp.StatusCode, http.StatusOK)
	assert.Equal(t, resp.Header.Get("Content-Type"), "text/event-stream")

	r := sse.NewReader(resp.Body)

	// Read the ready event.
	ev, err := r.Read()
	assert.NilError(t, err)
	assert.Equal(t, ev.Event, "ready")
	assert.Equal(t, ev.ID, "0")

	// Publish a notification.
	want := model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "created", Type: "task", ID: 42}},
		OrgID:     orgID,
		Time:      time.Now().Truncate(time.Second),
	}
	err = ps.Publish(ctx, want)
	assert.NilError(t, err)

	// Read the change event.
	ev, err = r.Read()
	assert.NilError(t, err)
	assert.Equal(t, ev.Event, "change")
	assert.Equal(t, ev.ID, "1")

	var got model.Notification
	err = json.Unmarshal(ev.Data, &got)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, want)
}

func TestSSE_SelfNotificationDelivered(t *testing.T) {
	t.Parallel()

	ps := pubsub.NewLocalPubSub()
	const orgID int64 = 1
	srv := New(Options{Subscriber: ps})

	ts := httptest.NewServer(apiauth.WithTestUser(srv.Handler(), &apiauth.UserInfo{ID: "u", OrgID: orgID, Type: apiauth.AuthTypeApp}))
	defer ts.Close()

	req, err := http.NewRequestWithContext(t.Context(), "GET", ts.URL+"/events", nil)
	assert.NilError(t, err)

	resp, err := http.DefaultClient.Do(req)
	assert.NilError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, resp.StatusCode, http.StatusOK)

	r := sse.NewReader(resp.Body)

	// Read the ready event.
	ev, err := r.Read()
	assert.NilError(t, err)
	assert.Equal(t, ev.Event, "ready")

	// Publish a notification from the same user — server delivers all
	// notifications; client-side filtering handles deduplication.
	want := model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "created", Type: "task", ID: 1}},
		OrgID:     orgID,
		UserID:    "u",
		Time:      time.Now().Truncate(time.Second),
	}
	err = ps.Publish(t.Context(), want)
	assert.NilError(t, err)

	// Should receive the notification even though it's from the same user.
	ev, err = r.Read()
	assert.NilError(t, err)
	assert.Equal(t, ev.Event, "change")

	var got model.Notification
	err = json.Unmarshal(ev.Data, &got)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, want)
}

func TestSSE_RunnerFilter(t *testing.T) {
	t.Parallel()

	ps := pubsub.NewLocalPubSub()
	const orgID int64 = 1
	srv := New(Options{Subscriber: ps})

	ts := httptest.NewServer(apiauth.WithTestUser(srv.Handler(), &apiauth.UserInfo{ID: "u", OrgID: orgID, Type: apiauth.AuthTypeApp}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/events?runner=r1", nil)
	assert.NilError(t, err)

	resp, err := http.DefaultClient.Do(req)
	assert.NilError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, resp.StatusCode, http.StatusOK)

	r := sse.NewReader(resp.Body)

	// Read the ready event.
	ev, err := r.Read()
	assert.NilError(t, err)
	assert.Equal(t, ev.Event, "ready")

	// Notification with no runner (no pending work) should be skipped.
	err = ps.Publish(ctx, model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "updated", Type: "task", ID: 1}},
		OrgID:     orgID,
	})
	assert.NilError(t, err)

	// Notification for a different runner should be skipped.
	err = ps.Publish(ctx, model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "updated", Type: "task", ID: 2}},
		OrgID:     orgID,
		Runner:    "r2",
	})
	assert.NilError(t, err)

	// Notification for this runner should be delivered.
	want := model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "restarted", Type: "task", ID: 3}},
		OrgID:     orgID,
		Runner:    "r1",
		Time:      time.Now().Truncate(time.Second),
	}
	err = ps.Publish(ctx, want)
	assert.NilError(t, err)

	// Should only receive the r1 notification.
	ev, err = r.Read()
	assert.NilError(t, err)
	assert.Equal(t, ev.Event, "change")

	var got model.Notification
	err = json.Unmarshal(ev.Data, &got)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, want)
}

func TestSSE_OrgIsolation(t *testing.T) {
	t.Parallel()

	ps := pubsub.NewLocalPubSub()
	const orgA, orgB int64 = 1, 2
	srv := New(Options{Subscriber: ps})

	ts := httptest.NewServer(apiauth.WithTestUser(srv.Handler(), &apiauth.UserInfo{ID: "u", OrgID: orgB, Type: apiauth.AuthTypeApp}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/events", nil)
	assert.NilError(t, err)

	resp, err := http.DefaultClient.Do(req)
	assert.NilError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, resp.StatusCode, http.StatusOK)

	r := sse.NewReader(resp.Body)

	// Read the ready event.
	ev, err := r.Read()
	assert.NilError(t, err)
	assert.Equal(t, ev.Event, "ready")

	// Publish to org A only.
	err = ps.Publish(ctx, model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "created", Type: "task", ID: 1}},
		OrgID:     orgA,
	})
	assert.NilError(t, err)

	// Publish to org B.
	wantB := model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "updated", Type: "task", ID: 2}},
		OrgID:     orgB,
		Time:      time.Now().Truncate(time.Second),
	}
	err = ps.Publish(ctx, wantB)
	assert.NilError(t, err)

	// Should only receive the org B notification.
	ev, err = r.Read()
	assert.NilError(t, err)
	assert.Equal(t, ev.Event, "change")

	var got model.Notification
	err = json.Unmarshal(ev.Data, &got)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, wantB)
}

func TestSSE_CookieAuth(t *testing.T) {
	t.Parallel()

	ps := pubsub.NewLocalPubSub()
	const orgID int64 = 1
	srv := New(Options{
		Subscriber:  ps,
		OrgResolver: allowOrgResolver("u", orgID),
	})

	// Cookie auth has no org id on the caller; it comes from the org_id
	// query parameter and is validated by the resolver.
	ts := httptest.NewServer(apiauth.WithTestUser(srv.Handler(), &apiauth.UserInfo{ID: "u", Type: apiauth.AuthTypeCookie}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/events?org_id=%d", ts.URL, orgID), nil)
	assert.NilError(t, err)

	resp, err := http.DefaultClient.Do(req)
	assert.NilError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, resp.StatusCode, http.StatusOK)

	r := sse.NewReader(resp.Body)

	// Read the ready event.
	ev, err := r.Read()
	assert.NilError(t, err)
	assert.Equal(t, ev.Event, "ready")

	// Publish a notification for the resolved org.
	want := model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "created", Type: "task", ID: 42}},
		OrgID:     orgID,
		Time:      time.Now().Truncate(time.Second),
	}
	err = ps.Publish(ctx, want)
	assert.NilError(t, err)

	ev, err = r.Read()
	assert.NilError(t, err)
	assert.Equal(t, ev.Event, "change")

	var got model.Notification
	err = json.Unmarshal(ev.Data, &got)
	assert.NilError(t, err)
	assert.DeepEqual(t, got, want)
}

func TestSSE_CookieAuthForbidden(t *testing.T) {
	t.Parallel()

	ps := pubsub.NewLocalPubSub()
	srv := New(Options{
		Subscriber:  ps,
		OrgResolver: allowOrgResolver("u", 1),
	})

	ts := httptest.NewServer(apiauth.WithTestUser(srv.Handler(), &apiauth.UserInfo{ID: "u", Type: apiauth.AuthTypeCookie}))
	defer ts.Close()

	// Request an org the user is not a member of.
	resp, err := http.Get(ts.URL + "/events?org_id=2")
	assert.NilError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, resp.StatusCode, http.StatusForbidden)
}

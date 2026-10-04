package awsmicrovm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gritzapp/gritz/internal/x/sse"
	"gotest.tools/v3/assert"
)

func TestCreateMicrovmAuthTokenSignsAndPosts(t *testing.T) {
	var gotPath, gotAuth, gotMethod string
	var gotBody struct {
		ExpirationMinutes int             `json:"expirationInMinutes"`
		AllowedPorts      json.RawMessage `json:"allowedPorts"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"authToken":{"X-aws-proxy-auth":"tok-123"}}`))
	}))
	defer srv.Close()

	out, err := newTestClient(srv).CreateMicrovmAuthToken(context.Background(), &CreateMicrovmAuthTokenInput{
		MicrovmID:         "mvm-1",
		ExpirationMinutes: 30,
		AllowedPorts:      []AllowedPort{{All: true}},
	})
	assert.NilError(t, err)
	assert.Equal(t, out.Token, "tok-123")
	assert.Equal(t, gotMethod, http.MethodPost)
	assert.Equal(t, gotPath, "/2025-09-09/microvms/mvm-1/auth-token")
	assert.Assert(t, strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=AKID/"), "auth header = %q", gotAuth)
	assert.Equal(t, gotBody.ExpirationMinutes, 30)
	assert.Equal(t, string(gotBody.AllowedPorts), `[{"allPorts":{}}]`)
}

func TestCreateMicrovmAuthTokenAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"__type":"ResourceNotFoundException","message":"microvm mvm-x not found"}`))
	}))
	defer srv.Close()

	_, err := newTestClient(srv).CreateMicrovmAuthToken(context.Background(), &CreateMicrovmAuthTokenInput{MicrovmID: "mvm-x"})
	var apiErr *APIError
	assert.Assert(t, errors.As(err, &apiErr), "error is not *APIError: %v", err)
	assert.DeepEqual(t, apiErr, &APIError{
		Op:         "CreateMicrovmAuthToken",
		StatusCode: http.StatusNotFound,
		Code:       "ResourceNotFoundException",
		Message:    "microvm mvm-x not found",
	})
	assert.Assert(t, IsNotFound(err))
}

func TestAllowedPortMarshal(t *testing.T) {
	data, err := json.Marshal([]AllowedPort{{All: true}, {Port: 8080}})
	assert.NilError(t, err)
	assert.Equal(t, string(data), `[{"allPorts":{}},{"port":8080}]`)
}

func TestNewProxyRequestSetsURLAndHeader(t *testing.T) {
	req, err := NewProxyRequest(context.Background(), "mvm-1.example.com", "tok-123", http.MethodGet, "/health", nil)
	assert.NilError(t, err)
	assert.Equal(t, req.URL.String(), "https://mvm-1.example.com/health")
	assert.Equal(t, req.Header.Get(ProxyAuthHeader), "tok-123")

	// An endpoint that already carries a scheme is used as-is.
	req, err = NewProxyRequest(context.Background(), "https://mvm-1.example.com/", "tok", http.MethodPost, "/run", nil)
	assert.NilError(t, err)
	assert.Equal(t, req.URL.String(), "https://mvm-1.example.com/run")
}

func TestNewProxyRequestStreamsSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(ProxyAuthHeader) != "tok-123" {
			t.Errorf("missing proxy auth header: %q", r.Header.Get(ProxyAuthHeader))
		}
		sw, err := sse.NewServerWriter(w)
		if err != nil {
			t.Errorf("NewServerWriter: %v", err)
			return
		}
		for _, data := range []string{"one", "two"} {
			if err := sw.Write(sse.Event{Data: []byte(data)}); err != nil {
				t.Errorf("write event: %v", err)
			}
		}
	}))
	defer srv.Close()

	req, err := NewProxyRequest(context.Background(), srv.URL, "tok-123", http.MethodGet, "/events", nil)
	assert.NilError(t, err)
	req.Header.Set("Accept", "text/event-stream")

	// The caller sends the request with its own client and parses the streamed
	// body itself (here with x/sse) — the helper does not parse it.
	resp, err := srv.Client().Do(req)
	assert.NilError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, resp.Header.Get("Content-Type"), "text/event-stream")

	var events []string
	rd := sse.NewReader(resp.Body)
	for {
		ev, err := rd.Read()
		assert.NilError(t, err)
		if len(ev.Data) == 0 {
			break
		}
		events = append(events, string(ev.Data))
	}
	assert.DeepEqual(t, events, []string{"one", "two"})
}

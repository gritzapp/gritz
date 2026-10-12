package lambdamicrovm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/driverserver"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/awsmicrovm"
	"gotest.tools/v3/assert"
)

const testTaskID = 1

// testEnv is a backend whose VMs all reach the same in-process driver server,
// served over TLS and HTTP/2 like the managed proxy, which rejects requests
// without a proxy token.
type testEnv struct {
	be    *Backend
	cloud *fakeCloud
	srv   *driverserver.Server

	mu sync.Mutex
	// auth records the proxy token header of every request the driver got,
	// and bearer its Authorization header.
	auth   []string
	bearer []string
}

func setup(t *testing.T, cfg *agent.Config) *testEnv {
	t.Helper()
	te := &testEnv{}
	dir := t.TempDir()
	store := agent.ConfigStore(filepath.Join(dir, "config"))
	assert.NilError(t, store.Save(testTaskID, cfg))
	gritz := &gritzclient.ClientMock{
		GetTaskFunc: func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
			return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: req.Id, Version: 1}}, nil
		},
		SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
			return &gritzv1.SubmitRunnerEventsResponse{}, nil
		},
		ListEventsByTaskFunc: func(_ context.Context, _ *gritzv1.ListEventsByTaskRequest) (*gritzv1.ListEventsByTaskResponse, error) {
			return &gritzv1.ListEventsByTaskResponse{NextPageToken: "tail"}, nil
		},
		ListLinksFunc: func(_ context.Context, _ *gritzv1.ListLinksRequest) (*gritzv1.ListLinksResponse, error) {
			return &gritzv1.ListLinksResponse{}, nil
		},
		AppendLogChunkFunc: func(_ context.Context, _ *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	srv, err := driverserver.New(driverserver.Options{
		RecordPath: filepath.Join(dir, "run.json"),
		LogPath:    filepath.Join(dir, "log"),
		Config:     store,
		FileRoot:   filepath.Join(dir, "root"),
		NewClient:  func(_, _ string) gritzclient.Client { return gritz },
		KeepAlive:  20 * time.Millisecond,
	})
	assert.NilError(t, err)
	te.srv = srv
	mux := http.NewServeMux()
	mux.Handle(srv.Handler())
	hs := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get(awsmicrovm.ProxyAuthHeader)
		te.mu.Lock()
		te.auth = append(te.auth, token)
		te.bearer = append(te.bearer, r.Header.Get("Authorization"))
		te.mu.Unlock()
		if token == "" {
			http.Error(w, "missing proxy token", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	hs.EnableHTTP2 = true
	hs.StartTLS()
	t.Cleanup(hs.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		assert.NilError(t, srv.Shutdown(ctx))
	})

	te.cloud = newFakeCloud(hs.URL)
	te.be, err = New(Options{Cloud: te.cloud, Transport: hs.Client().Transport, Region: "us-east-1"})
	assert.NilError(t, err)
	te.be.idleTimeout = time.Second
	te.be.stateTimeout = 5 * time.Second
	te.be.stateInterval = time.Millisecond
	return te
}

// tokens returns the distinct proxy tokens the driver was reached with.
func (te *testEnv) tokens() []string {
	te.mu.Lock()
	defer te.mu.Unlock()
	seen := map[string]bool{}
	var tokens []string
	for _, tok := range te.auth {
		if !seen[tok] {
			seen[tok] = true
			tokens = append(tokens, tok)
		}
	}
	return tokens
}

func testWorkspace() *workspace.Workspace {
	return &workspace.Workspace{
		LambdaMicroVM: &workspace.LambdaMicroVM{
			ImageIdentifier: "arn:aws:lambda:us-east-1:123:microvm-image/x",
			ExecutionRole:   "arn:aws:iam::123:role/x",
			EgressConnector: "arn:aws:lambda:us-east-1:aws:network-connector:aws-network-connector:INTERNET_EGRESS",
		},
	}
}

func testSpec(version int64) *backend.Spec {
	return &backend.Spec{
		TaskID:    testTaskID,
		Version:   version,
		Workspace: testWorkspace(),
		ServerURL: "http://gritz.test",
		Token:     "tok",
	}
}

// A cached token is reused until shortly before it expires, then re-minted.
func TestTokenTransport_Refresh(t *testing.T) {
	var minted int
	tt := &tokenTransport{
		base: http.DefaultTransport,
		mint: func(context.Context) (string, error) {
			minted++
			return "token", nil
		},
	}

	_, err := tt.get(t.Context())
	assert.NilError(t, err)
	_, err = tt.get(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, minted, 1)

	tt.expires = time.Now().Add(tokenRefreshMargin - time.Second)
	_, err = tt.get(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, minted, 2)
}

func TestValidateWorkspace(t *testing.T) {
	be, err := New(Options{Cloud: newFakeCloud(""), Region: "us-east-1"})
	assert.NilError(t, err)
	assert.NilError(t, be.ValidateWorkspace(testWorkspace()))

	tests := map[string]func(*workspace.LambdaMicroVM){
		"missing image":    func(c *workspace.LambdaMicroVM) { c.ImageIdentifier = "" },
		"missing role":     func(c *workspace.LambdaMicroVM) { c.ExecutionRole = "" },
		"missing egress":   func(c *workspace.LambdaMicroVM) { c.EgressConnector = "" },
		"duration too big": func(c *workspace.LambdaMicroVM) { c.MaxDurationSeconds = 99999 },
		"other region":     func(c *workspace.LambdaMicroVM) { c.Region = "eu-west-1" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			ws := testWorkspace()
			mutate(ws.LambdaMicroVM)
			assert.Assert(t, be.ValidateWorkspace(ws) != nil)
		})
	}
}

package configfile

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

func TestTokenSource_WritesBack(t *testing.T) {
	t.Parallel()
	// Arrange - a token endpoint that rotates to "new", and a stored refresh
	// token without an iat, which counts as more than a day old
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at","token_type":"Bearer","expires_in":300,"refresh_token":"new"}`))
	}))
	t.Cleanup(srv.Close)
	path := filepath.Join(t.TempDir(), Name)
	assert.NilError(t, Login(path, "acme", Entry{ID: 12, RefreshToken: "old", Server: srv.URL}))
	entry := &Entry{ID: 12, RefreshToken: "old", Server: srv.URL, Name: "acme", Path: path}

	// Act
	_, err := entry.TokenSource(t.Context()).Token()

	// Assert
	assert.NilError(t, err)
	f, err := Read(path)
	assert.NilError(t, err)
	assert.Equal(t, f.Orgs["acme"].RefreshToken, "new")
}

func TestTokenSource_LoginExpired(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid refresh_token", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	entry := &Entry{ID: 12, RefreshToken: "old", Server: srv.URL, Name: "acme", Path: filepath.Join(t.TempDir(), Name)}

	_, err := entry.TokenSource(t.Context()).Token()

	assert.Error(t, err, "org entry \"acme\": login expired or revoked: run `gritz login --name acme --server "+srv.URL+"`")
}

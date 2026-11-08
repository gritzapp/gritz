package configfile

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/golden"
)

// Regenerate the golden with: go test ./internal/configfile/ -run TestTokenSource_WriteBack -update
func TestTokenSource_WriteBack(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		entryName    string
		refreshToken string
		golden       string // empty means the file is unchanged
	}{
		{name: "stored token matches", entryName: "acme", refreshToken: "old", golden: "write-back.golden"},
		{name: "token changed", entryName: "acme", refreshToken: "other"},
		{name: "entry gone", entryName: "labs", refreshToken: "old"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - a token endpoint that rotates to "new", and refresh
			// tokens without an iat, which count as more than a day old
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"at","token_type":"Bearer","expires_in":300,"refresh_token":"new"}`))
			}))
			t.Cleanup(srv.Close)
			input, err := os.ReadFile(filepath.Join("testdata", "write-back.yml"))
			assert.NilError(t, err)
			path := filepath.Join(t.TempDir(), Name)
			assert.NilError(t, os.WriteFile(path, input, 0o600))
			entry := &Entry{ID: 12, RefreshToken: tt.refreshToken, Server: srv.URL, Name: tt.entryName, Path: path}

			// Act
			_, err = entry.TokenSource(t.Context()).Token()

			// Assert
			assert.NilError(t, err)
			got, err := os.ReadFile(path)
			assert.NilError(t, err)
			if tt.golden == "" {
				assert.Equal(t, string(got), string(input))
				return
			}
			golden.Assert(t, string(got), tt.golden)
		})
	}
}

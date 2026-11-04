package server_test

import (
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gritzapp/gritz/internal/server"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
)

func TestHealthHandler(t *testing.T) {
	t.Parallel()
	// Arrange
	st := teststore.New(t)
	rec := httptest.NewRecorder()

	// Act
	server.HealthHandler(slog.Default(), st).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	// Assert
	assert.Equal(t, rec.Code, http.StatusOK)
}

func TestHealthHandler_DatabaseUnavailable(t *testing.T) {
	t.Parallel()
	// Arrange - a closed database fails every ping, as a lost connection would
	db, err := sql.Open("pgx", "postgres://localhost:1/gritz")
	assert.NilError(t, err)
	assert.NilError(t, db.Close())
	rec := httptest.NewRecorder()

	// Act
	server.HealthHandler(slog.Default(), store.New(db)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	// Assert
	assert.Equal(t, rec.Code, http.StatusServiceUnavailable)
}

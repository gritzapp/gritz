package apibadge_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gritzapp/gritz/internal/server/apibadge"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/golden"
)

func TestHandler(t *testing.T) {
	t.Parallel()
	// Arrange
	mux := http.NewServeMux()
	mux.Handle(apibadge.Route, apibadge.Handler())
	rec := httptest.NewRecorder()

	// Act
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/badge/2474.json", nil))

	// Assert
	assert.Equal(t, rec.Code, http.StatusOK)
	assert.Equal(t, rec.Header().Get("Content-Type"), "application/json")
	golden.Assert(t, rec.Body.String(), "badge.golden")
}

func TestHandler_InvalidID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		path string
	}{
		{"no suffix", "/badge/2474"},
		{"wrong suffix", "/badge/2474.svg"},
		{"empty", "/badge/.json"},
		{"not a number", "/badge/abc.json"},
		{"zero", "/badge/0.json"},
		{"negative", "/badge/-1.json"},
		{"signed", "/badge/+1.json"},
		{"leading zero", "/badge/01.json"},
		{"overflow", "/badge/99999999999999999999.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			mux := http.NewServeMux()
			mux.Handle(apibadge.Route, apibadge.Handler())
			rec := httptest.NewRecorder()

			// Act
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))

			// Assert
			assert.Equal(t, rec.Code, http.StatusNotFound)
		})
	}
}

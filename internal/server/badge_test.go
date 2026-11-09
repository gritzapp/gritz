package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gritzapp/gritz/internal/server"
	"gotest.tools/v3/assert"
)

func TestBadgeHandler(t *testing.T) {
	t.Parallel()
	// Arrange
	mux := http.NewServeMux()
	mux.Handle(server.BadgeRoute, server.BadgeHandler())
	rec := httptest.NewRecorder()

	// Act
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/badge/2474.json", nil))

	// Assert
	assert.Equal(t, rec.Code, http.StatusOK)
	assert.Equal(t, rec.Header().Get("Content-Type"), "application/json")
	assert.Equal(t, rec.Body.String(), `{"schemaVersion":1,"label":"","message":"2474","color":"black","labelColor":"black","logoSvg":"<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 32 32\" fill=\"#fff\"><rect x=\"8\" y=\"2\" width=\"16\" height=\"7\" rx=\"2\"/><rect x=\"2\" y=\"8\" width=\"7\" height=\"15\" rx=\"2\"/><rect x=\"17\" y=\"15\" width=\"13\" height=\"6\" rx=\"2\"/><rect x=\"8\" y=\"23\" width=\"9\" height=\"7\" rx=\"2\"/></svg>"}`+"\n")
}

func TestBadgeHandler_InvalidID(t *testing.T) {
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
			mux.Handle(server.BadgeRoute, server.BadgeHandler())
			rec := httptest.NewRecorder()

			// Act
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))

			// Assert
			assert.Equal(t, rec.Code, http.StatusNotFound)
		})
	}
}

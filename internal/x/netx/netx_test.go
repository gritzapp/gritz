package netx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

func TestSplit(t *testing.T) {
	tests := []struct {
		addr    string
		network string
		address string
	}{
		{addr: ":8080", network: "tcp", address: ":8080"},
		{addr: "127.0.0.1:8080", network: "tcp", address: "127.0.0.1:8080"},
		{addr: "unix:///gritz/driver.sock", network: "unix", address: "/gritz/driver.sock"},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			network, address := Split(tt.addr)
			assert.Equal(t, network, tt.network)
			assert.Equal(t, address, tt.address)
		})
	}
}

// Listen replaces a stale socket file, and closing the listener removes it.
func TestListen_Unix(t *testing.T) {
	// Arrange - a file left behind at the socket path
	path := filepath.Join(t.TempDir(), "driver.sock")
	assert.NilError(t, os.WriteFile(path, nil, 0o600))

	// Act
	l, err := Listen("unix://" + path)
	assert.NilError(t, err)
	conn, dialErr := Dial(t.Context(), "unix://"+path)
	closeErr := l.Close()

	// Assert
	assert.NilError(t, dialErr)
	assert.NilError(t, conn.Close())
	assert.NilError(t, closeErr)
	_, err = os.Stat(path)
	assert.Assert(t, errors.Is(err, fs.ErrNotExist), "socket file not removed: %v", err)
}

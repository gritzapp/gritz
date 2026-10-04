package githubx_test

import (
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/gritzapp/gritz/internal/x/githubx"
	"gotest.tools/v3/assert"
)

func testKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	assert.NilError(t, err)
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

func TestParsePrivateKey(t *testing.T) {
	valid := testKeyPEM(t)

	tests := map[string][]byte{
		"plain PEM":              valid,
		"escaped newlines":       []byte(strings.ReplaceAll(string(valid), "\n", `\n`)),
		"quoted and escaped":     []byte(`"` + strings.ReplaceAll(string(valid), "\n", `\n`) + `"`),
		"surrounding whitespace": []byte("  \n" + string(valid) + "\n  "),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			key, err := githubx.ParsePrivateKey(input)
			assert.NilError(t, err)
			assert.Assert(t, key != nil)
		})
	}
}

func TestParsePrivateKey_Invalid(t *testing.T) {
	_, err := githubx.ParsePrivateKey([]byte("not a key"))
	assert.ErrorContains(t, err, "Key must be a PEM encoded")
}

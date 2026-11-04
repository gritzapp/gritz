package store_test

import (
	"strings"
	"testing"

	"github.com/gritzapp/gritz/internal/store"
	"gotest.tools/v3/assert"
)

func TestMigrateInvalidURLOmitsPassword(t *testing.T) {
	t.Parallel()
	// Arrange - an unescaped '%' makes the URL unparseable.
	const password = "hunter2%zz"
	// Act
	err := store.Migrate("postgres://postgres:" + password + "@localhost:5432/postgres")
	// Assert
	assert.Assert(t, !strings.Contains(err.Error(), "hunter2"), err.Error())
	assert.Assert(t, !strings.Contains(err.Error(), "%zz"), err.Error())
}

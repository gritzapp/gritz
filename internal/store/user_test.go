package store_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
)

// createUser creates a user with the given email.
func createUser(t *testing.T, s *store.Store, email string) *model.User {
	t.Helper()
	u := &model.User{ID: uuid.NewString(), Email: email}
	assert.NilError(t, s.CreateUser(t.Context(), nil, u))
	return u
}

func TestGetUserByEmail_IgnoresCase(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	email := uuid.NewString() + "@Example.com"
	u := createUser(t, s, email)

	// Act
	got, err := s.GetUserByEmail(t.Context(), nil, strings.ToUpper(email))

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, got.ID, u.ID)
	assert.Equal(t, got.Email, email)
}

func TestGetUserByEmail_NotFound(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)

	// Act
	_, err := s.GetUserByEmail(t.Context(), nil, uuid.NewString()+"@example.com")

	// Assert
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestGetUserByEmail_CaseVariants(t *testing.T) {
	t.Parallel()
	// Arrange - two users whose emails differ only in case.
	s := teststore.New(t)
	local := uuid.NewString()
	lower := createUser(t, s, local+"@example.com")
	upper := createUser(t, s, strings.ToUpper(local)+"@example.com")

	tests := []struct {
		name  string
		email string
		want  string
		err   error
	}{
		{"exact lower", lower.Email, lower.ID, nil},
		{"exact upper", upper.Email, upper.ID, nil},
		{"neither", local + "@EXAMPLE.COM", "", store.ErrAmbiguousEmail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := s.GetUserByEmail(t.Context(), nil, tt.email)

			// Assert
			if tt.err != nil {
				assert.ErrorIs(t, err, tt.err)
				return
			}
			assert.NilError(t, err)
			assert.Equal(t, got.ID, tt.want)
		})
	}
}

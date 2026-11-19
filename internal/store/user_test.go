package store_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
)

func TestCreateUser_LowerCasesEmail(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	local := uuid.NewString()
	u := &model.User{ID: uuid.NewString(), Email: strings.ToUpper(local) + "@Example.com"}

	// Act
	err := s.CreateUser(t.Context(), nil, u)

	// Assert
	assert.NilError(t, err)
	got, err := s.GetUser(t.Context(), nil, u.ID)
	assert.NilError(t, err)
	assert.Equal(t, got.Email, local+"@example.com")
	assert.Equal(t, u.Email, local+"@example.com")
}

func TestUpsertUser_LowerCasesEmail(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	local := uuid.NewString()
	u := &model.User{ID: uuid.NewString(), Email: strings.ToUpper(local) + "@Example.com"}

	// Act
	err := s.UpsertUser(t.Context(), nil, u)

	// Assert
	assert.NilError(t, err)
	got, err := s.GetUser(t.Context(), nil, u.ID)
	assert.NilError(t, err)
	assert.Equal(t, got.Email, local+"@example.com")
}

func TestGetUserByEmail_IgnoresCase(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	u := &model.User{ID: uuid.NewString(), Email: uuid.NewString() + "@example.com"}
	assert.NilError(t, s.CreateUser(t.Context(), nil, u))

	// Act
	got, err := s.GetUserByEmail(t.Context(), nil, strings.ToUpper(u.Email))

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, got.ID, u.ID)
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

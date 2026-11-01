package sqlite_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/golden"

	"github.com/gritzapp/gritz/internal/x/sqlite"
)

// dump renders the database's user_version, then the sql of every
// sqlite_master object in name order, one per line.
func dump(t *testing.T, db *sql.DB) string {
	t.Helper()
	var version int
	assert.NilError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	var b strings.Builder
	fmt.Fprintf(&b, "user_version = %d\n", version)
	rows, err := db.Query(`SELECT sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY name`)
	assert.NilError(t, err)
	defer rows.Close()
	for rows.Next() {
		var stmt string
		assert.NilError(t, rows.Scan(&stmt))
		b.WriteString(stmt + "\n")
	}
	assert.NilError(t, rows.Err())
	return b.String()
}

// TestMigrate, TestMigrate_UpToDate and TestMigrate_NewFile end in the same
// state, so they share one golden. Regenerate it with:
// go test ./internal/x/sqlite/ -run TestMigrate -update

func TestMigrate(t *testing.T) {
	t.Parallel()
	// Arrange
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"), sqlite.Options{})
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, db.Close()) })
	fsys := fstest.MapFS{
		"0001_a.sql": {Data: []byte(`CREATE TABLE a (id INTEGER PRIMARY KEY);`)},
		"0002_b.sql": {Data: []byte(`CREATE TABLE b (id INTEGER PRIMARY KEY);`)},
	}

	// Act
	err = sqlite.Migrate(db, fsys)

	// Assert
	assert.NilError(t, err)
	golden.Assert(t, dump(t, db), "migrate.golden")
}

func TestMigrate_UpToDate(t *testing.T) {
	t.Parallel()
	// Arrange
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"), sqlite.Options{})
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, db.Close()) })
	fsys := fstest.MapFS{
		"0001_a.sql": {Data: []byte(`CREATE TABLE a (id INTEGER PRIMARY KEY);`)},
		"0002_b.sql": {Data: []byte(`CREATE TABLE b (id INTEGER PRIMARY KEY);`)},
	}
	assert.NilError(t, sqlite.Migrate(db, fsys))

	// Act - reapplying either CREATE TABLE would fail.
	err = sqlite.Migrate(db, fsys)

	// Assert
	assert.NilError(t, err)
	golden.Assert(t, dump(t, db), "migrate.golden")
}

func TestMigrate_NewFile(t *testing.T) {
	t.Parallel()
	// Arrange
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"), sqlite.Options{})
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, db.Close()) })
	fsys := fstest.MapFS{
		"0001_a.sql": {Data: []byte(`CREATE TABLE a (id INTEGER PRIMARY KEY);`)},
	}
	assert.NilError(t, sqlite.Migrate(db, fsys))
	fsys["0002_b.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE b (id INTEGER PRIMARY KEY);`)}

	// Act - reapplying 0001 would fail.
	err = sqlite.Migrate(db, fsys)

	// Assert
	assert.NilError(t, err)
	golden.Assert(t, dump(t, db), "migrate.golden")
}

func TestMigrate_DatabaseNewer(t *testing.T) {
	t.Parallel()
	// Arrange
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"), sqlite.Options{})
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, db.Close()) })
	assert.NilError(t, sqlite.Migrate(db, fstest.MapFS{
		"0001_a.sql": {Data: []byte(`CREATE TABLE a (id INTEGER PRIMARY KEY);`)},
		"0002_b.sql": {Data: []byte(`CREATE TABLE b (id INTEGER PRIMARY KEY);`)},
	}))

	// Act
	err = sqlite.Migrate(db, fstest.MapFS{
		"0001_a.sql": {Data: []byte(`CREATE TABLE a (id INTEGER PRIMARY KEY);`)},
	})

	// Assert
	assert.ErrorContains(t, err, "schema version 2 is newer than the 1 migrations")
}

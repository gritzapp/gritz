package sqlite_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"

	"gotest.tools/v3/assert"

	"github.com/gritzapp/gritz/internal/x/sqlite"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"), sqlite.Options{MaxOpenConns: 1})
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, db.Close()) })
	return db
}

// schema returns the database's user_version and its table names, in name
// order.
func schema(t *testing.T, db *sql.DB) (int, []string) {
	t.Helper()
	var version int
	assert.NilError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	assert.NilError(t, err)
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		assert.NilError(t, rows.Scan(&name))
		tables = append(tables, name)
	}
	assert.NilError(t, rows.Err())
	return version, tables
}

func TestMigrate(t *testing.T) {
	t.Parallel()
	// Arrange
	db := openDB(t)
	fsys := fstest.MapFS{
		"0001_a.sql": {Data: []byte(`CREATE TABLE a (id INTEGER PRIMARY KEY);`)},
		"0002_b.sql": {Data: []byte(`CREATE TABLE b (id INTEGER PRIMARY KEY);`)},
	}

	// Act
	err := sqlite.Migrate(db, fsys)

	// Assert
	assert.NilError(t, err)
	version, tables := schema(t, db)
	assert.Equal(t, version, 2)
	assert.DeepEqual(t, tables, []string{"a", "b"})
}

func TestMigrate_UpToDate(t *testing.T) {
	t.Parallel()
	// Arrange
	db := openDB(t)
	fsys := fstest.MapFS{
		"0001_a.sql": {Data: []byte(`CREATE TABLE a (id INTEGER PRIMARY KEY);`)},
		"0002_b.sql": {Data: []byte(`CREATE TABLE b (id INTEGER PRIMARY KEY);`)},
	}
	assert.NilError(t, sqlite.Migrate(db, fsys))

	// Act - reapplying either CREATE TABLE would fail.
	err := sqlite.Migrate(db, fsys)

	// Assert
	assert.NilError(t, err)
	version, tables := schema(t, db)
	assert.Equal(t, version, 2)
	assert.DeepEqual(t, tables, []string{"a", "b"})
}

func TestMigrate_NewFile(t *testing.T) {
	t.Parallel()
	// Arrange
	db := openDB(t)
	fsys := fstest.MapFS{
		"0001_a.sql": {Data: []byte(`CREATE TABLE a (id INTEGER PRIMARY KEY);`)},
	}
	assert.NilError(t, sqlite.Migrate(db, fsys))
	fsys["0002_b.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE b (id INTEGER PRIMARY KEY);`)}

	// Act - reapplying 0001 would fail.
	err := sqlite.Migrate(db, fsys)

	// Assert
	assert.NilError(t, err)
	version, tables := schema(t, db)
	assert.Equal(t, version, 2)
	assert.DeepEqual(t, tables, []string{"a", "b"})
}

func TestMigrate_DatabaseNewer(t *testing.T) {
	t.Parallel()
	// Arrange
	db := openDB(t)
	assert.NilError(t, sqlite.Migrate(db, fstest.MapFS{
		"0001_a.sql": {Data: []byte(`CREATE TABLE a (id INTEGER PRIMARY KEY);`)},
		"0002_b.sql": {Data: []byte(`CREATE TABLE b (id INTEGER PRIMARY KEY);`)},
	}))

	// Act
	err := sqlite.Migrate(db, fstest.MapFS{
		"0001_a.sql": {Data: []byte(`CREATE TABLE a (id INTEGER PRIMARY KEY);`)},
	})

	// Assert
	assert.ErrorContains(t, err, "schema version 2 is newer than the 1 migrations")
}

// Package sqlite opens SQLite databases with the ncruces driver, which needs
// no cgo, and applies embedded migrations to them. Callers choose every
// connection setting; the package only encodes them.
package sqlite

import (
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"strconv"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver" // registers the "sqlite3" database/sql driver
	_ "github.com/ncruces/go-sqlite3/embed"  // the SQLite build the driver runs
)

// JournalMode is a PRAGMA journal_mode value.
type JournalMode string

const (
	JournalModeDelete JournalMode = "delete"
	JournalModeWAL    JournalMode = "wal"
)

// Synchronous is a PRAGMA synchronous value.
type Synchronous string

const (
	SynchronousOff    Synchronous = "off"
	SynchronousNormal Synchronous = "normal"
	SynchronousFull   Synchronous = "full"
)

// Options are the connection settings Open applies to every connection,
// encoded as _pragma DSN parameters.
type Options struct {
	JournalMode JournalMode   // e.g. JournalModeWAL; "" leaves SQLite's default
	Synchronous Synchronous   // e.g. SynchronousFull; "" leaves the default
	BusyTimeout time.Duration // 0 leaves the default
}

// Open opens the database file at path with the ncruces (no-cgo) driver.
func Open(path string, opts Options) (*sql.DB, error) {
	// The pragmas go in the DSN so they apply to every connection the pool
	// opens, not only the first.
	var pragmas []string
	if opts.JournalMode != "" {
		pragmas = append(pragmas, "journal_mode("+string(opts.JournalMode)+")")
	}
	if opts.Synchronous != "" {
		pragmas = append(pragmas, "synchronous("+string(opts.Synchronous)+")")
	}
	if opts.BusyTimeout != 0 {
		pragmas = append(pragmas, "busy_timeout("+strconv.FormatInt(opts.BusyTimeout.Milliseconds(), 10)+")")
	}
	dsn := "file:" + (&url.URL{Path: path}).EscapedPath()
	if len(pragmas) > 0 {
		dsn += "?" + url.Values{"_pragma": pragmas}.Encode()
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %s: %w", path, err)
	}
	return db, nil
}

// Migrate applies the *.sql files in fsys that the database lacks, in name
// order, each in its own transaction, tracking progress in PRAGMA user_version.
// A database newer than fsys is an error.
func Migrate(db *sql.DB, fsys fs.FS) error {
	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return fmt.Errorf("sqlite: list migrations: %w", err)
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("sqlite: read schema version: %w", err)
	}
	if version > len(files) {
		return fmt.Errorf("sqlite: schema version %d is newer than the %d migrations", version, len(files))
	}
	for i, name := range files[version:] {
		if err := applyMigration(db, fsys, name, version+i+1); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(db *sql.DB, fsys fs.FS, name string, version int) error {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return fmt.Errorf("sqlite: read %s: %w", name, err)
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("sqlite: begin %s: %w", name, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(string(data)); err != nil {
		return fmt.Errorf("sqlite: apply %s: %w", name, err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
		return fmt.Errorf("sqlite: set schema version after %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit %s: %w", name, err)
	}
	return nil
}

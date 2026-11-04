package store

import (
	"errors"
	"net/url"

	"github.com/amacneil/dbmate/v2/pkg/dbmate"
	_ "github.com/amacneil/dbmate/v2/pkg/driver/postgres"
)

func Migrate(databaseURL string) error {
	u, err := url.Parse(databaseURL)
	if err != nil {
		// The parse error quotes the URL (or a fragment of it), which may
		// include the password.
		return errors.New("invalid database URL")
	}
	db := dbmate.New(u)
	db.FS = migrations
	db.MigrationsDir = []string{"sql/migrations"}
	db.AutoDumpSchema = false
	return db.Migrate()
}

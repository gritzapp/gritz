package configfile

import (
	"context"
	"log/slog"

	"github.com/gritzapp/gritz/internal/auth/cliauth"
	"golang.org/x/oauth2"
)

// TokenSource returns the entry's credentials: the key as a static token, or
// access tokens refreshed from RefreshToken, writing rotated refresh tokens
// back to Path once the stored one is more than a day old.
func (e *Entry) TokenSource(ctx context.Context) oauth2.TokenSource {
	return cliauth.TokenSource(ctx, cliauth.TokenSourceOptions{
		Server:       e.Server,
		Key:          e.Key,
		RefreshToken: e.RefreshToken,
		OnRefreshToken: func(token string) {
			// Any refresh token for the org is valid, so overwrite whatever
			// the entry holds, but don't recreate a deleted entry or add a
			// refresh token to one logged in again with a key.
			err := UpdateFile(e.Path, func(f *File) error {
				if fe, ok := f.Orgs[e.Name]; ok && fe.RefreshToken != "" {
					fe.RefreshToken = token
					f.Orgs[e.Name] = fe
				}
				return nil
			})
			if err != nil {
				slog.Warn("failed to write rotated refresh token", "path", e.Path, "org", e.Name, "err", err)
			}
		},
	})
}

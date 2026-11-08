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
	stored := e.RefreshToken
	return cliauth.TokenSource(ctx, cliauth.TokenSourceOptions{
		Server:       e.Server,
		Key:          e.Key,
		RefreshToken: e.RefreshToken,
		OnRefreshToken: func(token string) {
			// Leave the file alone if another process rotated the token
			// or the user logged in again: either is at least as fresh.
			err := UpdateFile(e.Path, func(f *File) error {
				fe, ok := f.Orgs[e.Name]
				if !ok || fe.RefreshToken != stored {
					return ErrSkipWrite
				}
				fe.RefreshToken = token
				f.Orgs[e.Name] = fe
				return nil
			})
			if err != nil {
				slog.Warn("failed to write rotated refresh token", "path", e.Path, "org", e.Name, "err", err)
				return
			}
			stored = token
		},
	})
}

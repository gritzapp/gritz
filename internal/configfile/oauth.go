package configfile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/gritzapp/gritz/internal/auth/cliauth"
	"golang.org/x/oauth2"
)

// TokenSource returns the entry's credentials: the key as a static token, or
// access tokens refreshed from RefreshToken, writing rotated refresh tokens
// back to Path once the stored one is more than a day old.
func (e *Entry) TokenSource(ctx context.Context) oauth2.TokenSource {
	if e.Key != "" {
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: e.Key})
	}
	stored := e.RefreshToken
	return &entrySource{entry: e, src: cliauth.TokenSource(ctx, cliauth.TokenSourceOptions{
		Server:       e.Server,
		RefreshToken: e.RefreshToken,
		OnRefreshToken: func(token string) {
			if err := UpdateEntry(e.Path, e.Name, rotate(stored, token)); err != nil {
				slog.Warn("failed to write rotated refresh token", "path", e.Path, "org", e.Name, "err", err)
				return
			}
			stored = token
		},
	})}
}

// entrySource names the entry in refresh errors, and tells the user to log
// in again when the login has expired.
type entrySource struct {
	entry *Entry
	src   oauth2.TokenSource
}

func (s *entrySource) Token() (*oauth2.Token, error) {
	tok, err := s.src.Token()
	if errors.Is(err, cliauth.ErrLoginExpired) {
		login := "gritz login --name " + s.entry.Name
		if s.entry.Server != DefaultServer {
			login += " --server " + s.entry.Server
		}
		return nil, fmt.Errorf("org entry %q: %w: run `%s`", s.entry.Name, err, login)
	}
	if err != nil {
		return nil, fmt.Errorf("org entry %q: refresh: %w", s.entry.Name, err)
	}
	return tok, nil
}

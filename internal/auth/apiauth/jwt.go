package apiauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/gritzapp/gritz/internal/auth/authscope"
)

// AppClaims contains the JWT claims for an app-issued token.
type AppClaims struct {
	jwt.RegisteredClaims
	Email  string           `json:"email"`
	Name   string           `json:"name"`
	OrgID  int64            `json:"org_id"`
	Role   string           `json:"role,omitempty"`
	Scopes authscope.Scopes `json:"scopes,omitempty"`
}

// AppTokenTTL is the default time-to-live for app JWTs.
const AppTokenTTL = 5 * time.Minute

// NewAppClaims creates AppClaims from a UserInfo.
func NewAppClaims(user *UserInfo) *AppClaims {
	now := time.Now()
	return &AppClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(AppTokenTTL)),
		},
		Email: user.Email,
		Name:  user.Name,
		OrgID: user.OrgID,
		// App JWTs are omnipotent within their org today; mint the admin
		// wildcard so behavior is unchanged once enforcement lands.
		Scopes: authscope.Admin(),
	}
}

// NewTaskTokenClaims builds the AppClaims for a server-minted task token: an
// ordinary app JWT carrying the task's org and a narrow scope set instead of the
// admin wildcard. There is no expiry — revocation is the task.archived scope
// predicate, not the clock (see proposals/implemented/eliminate-runner-socket-proxy.md
// §2/§3). The token verifies on the normal VerifyAppToken path like any other app
// JWT; its authority lives entirely in scopes.
func NewTaskTokenClaims(orgID int64, scopes authscope.Scopes) *AppClaims {
	return &AppClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt: jwt.NewNumericDate(time.Now()),
		},
		OrgID:  orgID,
		Scopes: scopes,
	}
}

// CreateAppPrivateKey generates a new Ed25519 private key for signing app JWTs.
func CreateAppPrivateKey() (ed25519.PrivateKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	return priv, nil
}

// DecodeAppKey decodes a hex-encoded Ed25519 seed (32 bytes) into a private key.
func DecodeAppKey(hexSeed string) (ed25519.PrivateKey, error) {
	if hexSeed == "" {
		return nil, nil
	}
	seed, err := hex.DecodeString(hexSeed)
	if err != nil {
		return nil, fmt.Errorf("failed to decode hex seed: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("seed must be %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// SignAppToken creates a JWT signed with the Ed25519 private key.
func SignAppToken(key ed25519.PrivateKey, claims *AppClaims) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	return token.SignedString(key)
}

// VerifyAppToken verifies and parses an app JWT, returning the claims.
func VerifyAppToken(key ed25519.PrivateKey, tokenStr string) (*AppClaims, error) {
	pubKey := key.Public()
	token, err := jwt.ParseWithClaims(tokenStr, &AppClaims{}, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodEd25519); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return pubKey, nil
	})
	if err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}
	claims, ok := token.Claims.(*AppClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}
	return claims, nil
}

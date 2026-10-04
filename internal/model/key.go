package model

import (
	"time"

	"github.com/gritzapp/gritz/internal/auth/authscope"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Key represents a user-generated API key.
type Key struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	TokenHash string           `json:"token_hash"`
	OrgID     int64            `json:"org_id"`
	ExpiresAt *time.Time       `json:"expires_at"`
	CreatedAt time.Time        `json:"created_at"`
	Scopes    authscope.Scopes `json:"scopes"`
}

// IsExpired returns true if the key has an expiration time that has passed.
func (k *Key) IsExpired() bool {
	return k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt)
}

// Proto converts a Key to its protobuf representation.
func (k *Key) Proto() *gritzv1.Key {
	pb := &gritzv1.Key{
		Id:        k.ID,
		Name:      k.Name,
		CreatedAt: timestamppb.New(k.CreatedAt),
	}
	if k.ExpiresAt != nil {
		pb.ExpiresAt = timestamppb.New(*k.ExpiresAt)
	}
	return pb
}

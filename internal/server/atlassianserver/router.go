//go:generate go tool moq -out router_moq_test.go . Router

package atlassianserver

import (
	"context"

	"github.com/gritzapp/gritz/internal/server/eventrouter"
)

// Router routes events to subscribed tasks.
type Router interface {
	Route(ctx context.Context, input eventrouter.InputEvent) (int, error)
}

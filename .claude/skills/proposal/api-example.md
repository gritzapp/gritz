### API

```go
package foo // github.com/gritzapp/gritz/internal/foo

// Store persists foos.
type Store struct{ ... }

// NewStore returns a Store backed by db.
func NewStore(db *pgxpool.Pool) *Store

// Create inserts f and sets its ID.
func (s *Store) Create(ctx context.Context, f *Foo) error

// List returns the task's foos, oldest first.
func (s *Store) List(ctx context.Context, taskID int64) ([]*Foo, error)
```

**How it fits:** the server constructs a `foo.Store`; the `CreateFoo` handler
calls `Create`.

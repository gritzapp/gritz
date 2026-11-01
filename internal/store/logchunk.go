package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store/sqlc"
	"github.com/gritzapp/gritz/internal/x/pagination"
)

// CreateLogChunk inserts a single log chunk and stamps it with its assigned ID
// and creation time. One chunk per statement is deliberate: the driver ships
// with a single in-flight sender, so id order is write order by construction —
// the ordering the transcript depends on rests on nothing but the bigserial.
// A caller wanting several inserts to commit together passes a tx.
func (s *Store) CreateLogChunk(ctx context.Context, tx *sql.Tx, chunk *model.LogChunk) error {
	createdAt := time.Now().UTC()
	id, err := s.q(tx).CreateLogChunk(ctx, sqlc.CreateLogChunkParams{
		OrgID:     chunk.OrgID,
		TaskID:    chunk.TaskID,
		Version:   chunk.Version,
		Data:      chunk.Data,
		CreatedAt: createdAt,
	})
	if err != nil {
		return err
	}
	chunk.ID = id
	chunk.CreatedAt = createdAt
	return nil
}

// logChunkCursor is the keyset a log chunk page token encodes. log_chunks.id is
// a unique monotonic bigserial, so it is a total order on its own — no
// tiebreaker.
type logChunkCursor struct {
	ID int64 `json:"i"`
}

// ListLogChunksByTaskPageParams mirrors the RPC's pagination fields as plain
// values so the handler can pass them through untouched.
type ListLogChunksByTaskPageParams struct {
	TaskID    int64
	OrgID     int64
	PageSize  int32  // 0 → default (50); max 200
	PageToken string // opaque cursor; empty for the newest page
}

// logChunkSource implements pagination.HistorySource for a task's log chunks:
// Older reads the Desc SQL and Newer the Asc SQL.
type logChunkSource struct {
	store  *Store
	tx     *sql.Tx
	params ListLogChunksByTaskPageParams
}

func (src logChunkSource) Older(ctx context.Context, before *logChunkCursor, limit int) ([]*model.LogChunk, error) {
	args := sqlc.ListLogChunksByTaskDescParams{
		TaskID:    src.params.TaskID,
		OrgID:     src.params.OrgID,
		UseCursor: before != nil,
		PageLimit: int32(limit), // int32 only at the sqlc boundary
	}
	if before != nil {
		args.CursorID = before.ID
	}
	rows, err := src.store.q(src.tx).ListLogChunksByTaskDesc(ctx, args)
	if err != nil {
		return nil, err
	}
	return toModelLogChunks(rows), nil
}

func (src logChunkSource) Newer(ctx context.Context, after *logChunkCursor, limit int) ([]*model.LogChunk, error) {
	args := sqlc.ListLogChunksByTaskAscParams{
		TaskID:    src.params.TaskID,
		OrgID:     src.params.OrgID,
		PageLimit: int32(limit), // int32 only at the sqlc boundary
	}
	// A nil cursor follows from the start: ids begin at 1.
	if after != nil {
		args.CursorID = after.ID
	}
	rows, err := src.store.q(src.tx).ListLogChunksByTaskAsc(ctx, args)
	if err != nil {
		return nil, err
	}
	return toModelLogChunks(rows), nil
}

func (src logChunkSource) Cursor(c *model.LogChunk) logChunkCursor {
	return logChunkCursor{ID: c.ID}
}

// ListLogChunksByTaskPage returns a history page of a task's log chunks, always
// oldest-first so a page's Data concatenates straight into transcript order. An
// empty PageToken returns the newest page — the end of the log, which is what a
// post-mortem reader wants first; OlderToken scrolls back toward the start of
// the log and FollowToken follows the chunks shipped after the page. It owns the
// cursor keyset (id), the page-size bounds, and the opaque token format. A bad
// PageSize or an undecodable PageToken surfaces as a wrapped
// pagination.ErrInvalidRequest; query failures surface as-is.
func (s *Store) ListLogChunksByTaskPage(ctx context.Context, tx *sql.Tx, p ListLogChunksByTaskPageParams) (*pagination.HistoryPage[*model.LogChunk], error) {
	// Same bounds as the timeline: chunks are dense and each one is small
	// relative to a page of events.
	return pagination.History(ctx, logChunkSource{store: s, tx: tx, params: p}, pagination.Options{
		DefaultPageSize: 50,
		MaxPageSize:     200,
		PageSize:        int(p.PageSize),
		PageToken:       p.PageToken,
	})
}

func toModelLogChunk(row sqlc.LogChunk) *model.LogChunk {
	return &model.LogChunk{
		ID:        row.ID,
		OrgID:     row.OrgID,
		TaskID:    row.TaskID,
		Version:   row.Version,
		Data:      row.Data,
		CreatedAt: row.CreatedAt,
	}
}

func toModelLogChunks(rows []sqlc.LogChunk) []*model.LogChunk {
	chunks := make([]*model.LogChunk, len(rows))
	for i, row := range rows {
		chunks[i] = toModelLogChunk(row)
	}
	return chunks
}

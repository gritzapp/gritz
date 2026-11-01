package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store/sqlc"
	"github.com/gritzapp/gritz/internal/x/pagination/v2"
)

func (s *Store) CreateEvent(ctx context.Context, tx *sql.Tx, event *model.Event) error {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return fmt.Errorf("marshal event payload: %w", err)
	}
	row, err := s.q(tx).CreateEvent(ctx, sqlc.CreateEventParams{
		TaskID:    event.TaskID,
		OrgID:     event.OrgID,
		Type:      event.Payload.Type(),
		Wake:      event.Wake,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	event.ID = row.ID
	event.XID = row.Xid
	return nil
}

func (s *Store) GetEvent(ctx context.Context, tx *sql.Tx, id int64, orgID int64) (*model.Event, error) {
	row, err := s.q(tx).GetEvent(ctx, sqlc.GetEventParams{
		ID:    id,
		OrgID: orgID,
	})
	if err != nil {
		return nil, err
	}
	return toModelEvent(row)
}

// ListEvents returns the org event feed newest-first. A nil or empty types
// filter returns all event types; a non-empty filter narrows to those types —
// e.g. the org external feed passes ['external'].
func (s *Store) ListEvents(ctx context.Context, tx *sql.Tx, limit int, orgID int64, types []string) ([]*model.Event, error) {
	// A nil slice encodes as SQL NULL (cardinality(NULL) is NULL, not 0), which
	// would filter everything out; coerce to an empty array so the "all types"
	// case matches the query's cardinality(...) = 0 guard.
	if types == nil {
		types = []string{}
	}
	rows, err := s.q(tx).ListEvents(ctx, sqlc.ListEventsParams{
		OrgID: orgID,
		Limit: int32(limit),
		Types: types,
	})
	if err != nil {
		return nil, err
	}
	return toModelEvents(rows)
}

func (s *Store) DeleteEvent(ctx context.Context, tx *sql.Tx, id int64, orgID int64) error {
	return s.q(tx).DeleteEvent(ctx, sqlc.DeleteEventParams{ID: id, OrgID: orgID})
}

// ListEventsByTask returns a task's events in chronological stream order. A nil
// or empty types filter returns all events; a non-empty filter narrows to those
// event types — e.g. the agent's brief passes instruction + external.
func (s *Store) ListEventsByTask(ctx context.Context, tx *sql.Tx, taskID int64, orgID int64, types []string) ([]*model.Event, error) {
	// A nil slice encodes as SQL NULL (cardinality(NULL) is NULL, not 0), which
	// would filter everything out; coerce to an empty array so the "all types"
	// case matches the query's cardinality(...) = 0 guard.
	if types == nil {
		types = []string{}
	}
	rows, err := s.q(tx).ListEventsByTask(ctx, sqlc.ListEventsByTaskParams{
		TaskID: taskID,
		OrgID:  orgID,
		Types:  types,
	})
	if err != nil {
		return nil, err
	}
	return toModelEvents(rows)
}

// eventCursor is the keyset an event page token encodes. events.id is a unique
// monotonic bigserial, so it is a total order on its own — no tiebreaker.
type eventCursor struct {
	ID int64 `json:"i"`
}

// ListEventsByTaskPageParams mirrors the RPC's pagination fields as plain values
// so the handler can pass them through untouched.
type ListEventsByTaskPageParams struct {
	TaskID    int64
	OrgID     int64
	Types     []string // nil/empty → all arms; a future arm-filtered page passes e.g. [external]
	PageSize  int32    // 0 → default (50); max 200
	PageToken string   // opaque cursor; empty for the newest page
}

// eventSource implements pagination.HistorySource for a task's events: Older
// reads the Desc SQL and Newer the Asc SQL.
type eventSource struct {
	store  *Store
	tx     *sql.Tx
	params ListEventsByTaskPageParams
}

// types returns the type filter as the queries take it. A nil slice encodes as
// SQL NULL (cardinality(NULL) is NULL, not 0), which would filter everything
// out; coerce to an empty array so the "all types" case matches the queries'
// cardinality(...) = 0 guard.
func (src eventSource) types() []string {
	if src.params.Types == nil {
		return []string{}
	}
	return src.params.Types
}

func (src eventSource) Older(ctx context.Context, before *eventCursor, limit int) ([]*model.Event, error) {
	args := sqlc.ListEventsByTaskDescParams{
		TaskID:    src.params.TaskID,
		OrgID:     src.params.OrgID,
		Types:     src.types(),
		UseCursor: before != nil,
		PageLimit: int32(limit), // int32 only at the sqlc boundary
	}
	if before != nil {
		args.CursorID = before.ID
	}
	rows, err := src.store.q(src.tx).ListEventsByTaskDesc(ctx, args)
	if err != nil {
		return nil, err
	}
	return toModelEvents(rows)
}

func (src eventSource) Newer(ctx context.Context, after *eventCursor, limit int) ([]*model.Event, error) {
	args := sqlc.ListEventsByTaskAscParams{
		TaskID:    src.params.TaskID,
		OrgID:     src.params.OrgID,
		Types:     src.types(),
		PageLimit: int32(limit), // int32 only at the sqlc boundary
	}
	// A nil cursor follows from the start: ids begin at 1.
	if after != nil {
		args.CursorID = after.ID
	}
	rows, err := src.store.q(src.tx).ListEventsByTaskAsc(ctx, args)
	if err != nil {
		return nil, err
	}
	return toModelEvents(rows)
}

func (src eventSource) Cursor(e *model.Event) eventCursor {
	return eventCursor{ID: e.ID}
}

// ListEventsByTaskPage returns a history page of a task's events, always
// oldest-first. An empty PageToken returns the newest page; OlderToken scrolls
// back toward the task's first event and FollowToken follows the events
// appended after the page. It owns the cursor keyset (id), the page-size
// bounds, and the opaque token format. A bad PageSize or an undecodable
// PageToken surfaces as a wrapped pagination.ErrInvalidRequest; query failures
// surface as-is.
func (s *Store) ListEventsByTaskPage(ctx context.Context, tx *sql.Tx, p ListEventsByTaskPageParams) (*pagination.HistoryPage[*model.Event], error) {
	// Timelines are dense (a report/lifecycle/tool row per step), so the default
	// and max pages are larger than the task list's.
	return pagination.History(ctx, eventSource{store: s, tx: tx, params: p}, pagination.Options{
		DefaultPageSize: 50,
		MaxPageSize:     200,
		PageSize:        int(p.PageSize),
		PageToken:       p.PageToken,
	})
}

// runnerEventCursor is the keyset a runner event page token encodes: the
// (xid, id) order ListRunnerEvents reads in. Rows written by one transaction
// share an xid, so id breaks the tie.
type runnerEventCursor struct {
	XID uint64 `json:"x"`
	ID  int64  `json:"i"`
}

// ListRunnerEventsParams mirrors the RPC's pagination fields as plain values
// so the handler can pass them through untouched.
type ListRunnerEventsParams struct {
	OrgID     int64
	Runner    string
	PageSize  int32  // 0 → default (200); max 1000
	PageToken string // opaque cursor; empty for the start of the log
}

// runnerEventSource implements pagination.CursorSource for the events of a
// runner's tasks: ascending (xid, id) after the cursor, below the xid
// watermark, with a nil cursor at the start of the log.
type runnerEventSource struct {
	store  *Store
	tx     *sql.Tx
	params ListRunnerEventsParams
}

func (src runnerEventSource) Query(ctx context.Context, after *runnerEventCursor, limit int) ([]*model.Event, error) {
	args := sqlc.ListRunnerEventsParams{
		Runner:    src.params.Runner,
		OrgID:     src.params.OrgID,
		PageLimit: int32(limit), // int32 only at the sqlc boundary
	}
	if after != nil {
		args.AfterXid = after.XID
		args.AfterID = after.ID
	}
	rows, err := src.store.q(src.tx).ListRunnerEvents(ctx, args)
	if err != nil {
		return nil, err
	}
	return toModelEvents(rows)
}

func (src runnerEventSource) Cursor(e *model.Event) runnerEventCursor {
	return runnerEventCursor{XID: e.XID, ID: e.ID}
}

// ListRunnerEvents returns a stream page of the events of the runner's tasks,
// in (xid, id) order, from transactions below the xid watermark. An empty
// PageToken is the start of the log, and the page's NextToken resumes after its
// last row, or keeps the request's position when the page is empty. A bad
// PageSize or an undecodable PageToken surfaces as a wrapped
// pagination.ErrInvalidRequest.
func (s *Store) ListRunnerEvents(ctx context.Context, tx *sql.Tx, p ListRunnerEventsParams) (*pagination.StreamPage[*model.Event], error) {
	return pagination.Stream(ctx, runnerEventSource{store: s, tx: tx, params: p}, pagination.Options{
		DefaultPageSize: 200,
		MaxPageSize:     1000,
		PageSize:        int(p.PageSize),
		PageToken:       p.PageToken,
	})
}

func toModelEvent(row sqlc.Event) (*model.Event, error) {
	payload, err := toEventPayload(row.Type, row.Payload)
	if err != nil {
		return nil, err
	}
	return &model.Event{
		ID:        row.ID,
		TaskID:    row.TaskID,
		OrgID:     row.OrgID,
		Wake:      row.Wake,
		Payload:   payload,
		CreatedAt: row.CreatedAt,
		XID:       row.Xid,
	}, nil
}

func toModelEvents(rows []sqlc.Event) ([]*model.Event, error) {
	events := make([]*model.Event, len(rows))
	for i, row := range rows {
		event, err := toModelEvent(row)
		if err != nil {
			return nil, err
		}
		events[i] = event
	}
	return events, nil
}

// toEventPayload picks the concrete model.EventPayload for a stored row by
// switching on its type discriminator, then decodes the jsonb body into it.
// This is the only place the events.type column is consumed — it is a storage
// detail, not a field on the Event value.
func toEventPayload(typ string, data []byte) (model.EventPayload, error) {
	switch typ {
	case model.EventTypeInstruction:
		var p model.InstructionPayload
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("unmarshal instruction payload: %w", err)
		}
		return &p, nil
	case model.EventTypeExternal:
		var p model.ExternalPayload
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("unmarshal external payload: %w", err)
		}
		return &p, nil
	case model.EventTypeReport:
		var p model.ReportPayload
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("unmarshal report payload: %w", err)
		}
		return &p, nil
	case model.EventTypeLifecycle:
		var p model.LifecyclePayload
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("unmarshal lifecycle payload: %w", err)
		}
		return &p, nil
	case model.EventTypeLink:
		var p model.LinkPayload
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("unmarshal link payload: %w", err)
		}
		return &p, nil
	case model.EventTypeCommand:
		var p model.CommandPayload
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("unmarshal command payload: %w", err)
		}
		return &p, nil
	default:
		return nil, fmt.Errorf("unknown event type %q", typ)
	}
}

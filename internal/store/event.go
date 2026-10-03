package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/icholy/gritz/internal/model"
	"github.com/icholy/gritz/internal/store/sqlc"
	"github.com/icholy/gritz/internal/x/pagination"
)

func (s *Store) CreateEvent(ctx context.Context, tx *sql.Tx, event *model.Event) error {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return fmt.Errorf("marshal event payload: %w", err)
	}
	id, err := s.q(tx).CreateEvent(ctx, sqlc.CreateEventParams{
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
	event.ID = id
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

// eventSource implements pagination.Source for a task's events, serving both
// walks from one Query: the forward walk (token.Backward == false, descending)
// → the Desc SQL (a nil cursor = newest page), and the backward walk
// (token.Backward == true, ascending live-follow) → the Asc SQL.
type eventSource struct {
	store  *Store
	tx     *sql.Tx
	params ListEventsByTaskPageParams
}

// Query serves both walks: token.Backward == true is the ascending live-follow
// (rows newer than the cursor); token.Backward == false the primary descending
// walk (a nil cursor = newest page).
func (src eventSource) Query(ctx context.Context, token pagination.Token[eventCursor], limit int) ([]*model.Event, error) {
	// A nil slice encodes as SQL NULL (cardinality(NULL) is NULL, not 0), which
	// would filter everything out; coerce to an empty array so the "all types"
	// case matches the query's cardinality(...) = 0 guard.
	types := src.params.Types
	if types == nil {
		types = []string{}
	}
	if token.Backward {
		rows, err := src.store.q(src.tx).ListEventsByTaskAsc(ctx, sqlc.ListEventsByTaskAscParams{
			TaskID:    src.params.TaskID,
			OrgID:     src.params.OrgID,
			Types:     types,
			CursorID:  token.Cursor.ID,
			PageLimit: int32(limit), // int32 only at the sqlc boundary
		})
		if err != nil {
			return nil, err
		}
		return toModelEvents(rows)
	}
	args := sqlc.ListEventsByTaskDescParams{
		TaskID:    src.params.TaskID,
		OrgID:     src.params.OrgID,
		Types:     types,
		UseCursor: token.Cursor != nil,
		PageLimit: int32(limit), // int32 only at the sqlc boundary
	}
	if token.Cursor != nil {
		args.CursorID = token.Cursor.ID
	}
	rows, err := src.store.q(src.tx).ListEventsByTaskDesc(ctx, args)
	if err != nil {
		return nil, err
	}
	return toModelEvents(rows)
}

func (src eventSource) Cursor(e *model.Event) eventCursor {
	return eventCursor{ID: e.ID}
}

// ListEventsByTaskPage returns a bidirectional keyset page of a task's events,
// always oldest-first (Options.Reverse). An empty PageToken returns the newest
// page; the returned NextToken continues toward older history (empties when
// exhausted) and PrevToken toward newer rows (stays populated on a non-empty
// page so the append-only tail can be followed). It owns the cursor keyset (id),
// the page-size bounds, and the opaque token format. A bad PageSize or an
// undecodable PageToken surfaces as a wrapped pagination.ErrInvalidRequest;
// query failures surface as-is.
func (s *Store) ListEventsByTaskPage(ctx context.Context, tx *sql.Tx, p ListEventsByTaskPageParams) (*pagination.Page[*model.Event], error) {
	// Timelines are dense (a report/lifecycle/tool row per step), so the default
	// and max pages are larger than the task list's; Reverse renders oldest-at-top.
	return pagination.List(ctx, pagination.Options[*model.Event, eventCursor]{
		DefaultPageSize: 50,
		MaxPageSize:     200,
		Reverse:         true,
		PageSize:        int(p.PageSize),
		PageToken:       p.PageToken,
		Source:          eventSource{store: s, tx: tx, params: p},
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
	default:
		return nil, fmt.Errorf("unknown event type %q", typ)
	}
}

package pagination

import "context"

// NewMockCursorSource returns a CursorSourceMock backed by an in-memory slice
// of int keys, treating each row as its own keyset (T and C are both int). Rows
// must be given in ascending key order. Query walks them ascending: keys above
// the cursor, lowest first; a nil cursor starts at the lowest key.
func NewMockCursorSource(rows []int) *CursorSourceMock[int, int] {
	return &CursorSourceMock[int, int]{
		QueryFunc: func(_ context.Context, after *int, limit int) ([]int, error) {
			return ascending(rows, after, limit), nil
		},
		CursorFunc: func(row int) int {
			return row
		},
	}
}

// NewMockHistorySource returns a HistorySourceMock backed by an in-memory slice
// of int keys, treating each row as its own keyset. Rows must be given in
// ascending key order. Older walks keys below the cursor, highest first (a nil
// cursor starts at the highest key); Newer walks keys above the cursor, lowest
// first (a nil cursor starts at the lowest key).
func NewMockHistorySource(rows []int) *HistorySourceMock[int, int] {
	return &HistorySourceMock[int, int]{
		OlderFunc: func(_ context.Context, before *int, limit int) ([]int, error) {
			var out []int
			for i := len(rows) - 1; i >= 0 && len(out) < limit; i-- {
				if before == nil || rows[i] < *before {
					out = append(out, rows[i])
				}
			}
			return out, nil
		},
		NewerFunc: func(_ context.Context, after *int, limit int) ([]int, error) {
			return ascending(rows, after, limit), nil
		},
		CursorFunc: func(row int) int {
			return row
		},
	}
}

// ascending returns up to limit rows above after, lowest first.
func ascending(rows []int, after *int, limit int) []int {
	var out []int
	for _, r := range rows {
		if len(out) == limit {
			break
		}
		if after == nil || r > *after {
			out = append(out, r)
		}
	}
	return out
}

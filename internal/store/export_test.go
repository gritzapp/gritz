package store

import "context"

// ScheduleExists reports whether schedule id is stored, in any org, so a test
// can check a deleted agent's schedules are gone.
func ScheduleExists(ctx context.Context, s *Store, id int64) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM schedules WHERE id = $1)`, id).Scan(&exists)
	return exists, err
}

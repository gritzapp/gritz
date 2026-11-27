package store

import "context"

// SchedulePosition returns the position of schedule id in its agent, which
// no model type carries yet.
func SchedulePosition(ctx context.Context, s *Store, id int64) (int, error) {
	var position int
	err := s.db.QueryRowContext(ctx, `SELECT position FROM schedules WHERE id = $1`, id).Scan(&position)
	return position, err
}

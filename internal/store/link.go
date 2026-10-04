package store

import (
	"context"
	"database/sql"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store/sqlc"
)

func (s *Store) CreateLink(ctx context.Context, tx *sql.Tx, link *model.Link) error {
	id, err := s.q(tx).CreateLink(ctx, sqlc.CreateLinkParams{
		TaskID:     link.TaskID,
		Relevance:  link.Relevance,
		Url:        link.URL,
		RoutingKey: link.RoutingKey,
		Title:      link.Title,
		Subscribe:  link.Subscribe,
		CreatedAt:  link.CreatedAt,
	})
	if err != nil {
		return err
	}
	link.ID = id
	return nil
}

func (s *Store) ListLinksByTask(ctx context.Context, tx *sql.Tx, taskID int64, orgID int64) ([]*model.Link, error) {
	rows, err := s.q(tx).ListLinksByTask(ctx, sqlc.ListLinksByTaskParams{
		TaskID: taskID,
		OrgID:  orgID,
	})
	if err != nil {
		return nil, err
	}
	return toModelLinks(rows), nil
}

func (s *Store) DeleteLink(ctx context.Context, tx *sql.Tx, id int64) error {
	return s.q(tx).DeleteLink(ctx, id)
}

// FindSubscribedLinksForOrgs returns subscribed links matching the routing key,
// scoped to the given orgs, grouped by org ID.
func (s *Store) FindSubscribedLinksForOrgs(ctx context.Context, tx *sql.Tx, routingKey string, orgIDs []int64) (map[int64][]*model.Link, error) {
	rows, err := s.q(tx).FindSubscribedLinksForOrgs(ctx, sqlc.FindSubscribedLinksForOrgsParams{
		RoutingKey: routingKey,
		OrgIds:     orgIDs,
	})
	if err != nil {
		return nil, err
	}
	result := make(map[int64][]*model.Link)
	for _, row := range rows {
		result[row.OrgID] = append(result[row.OrgID], &model.Link{
			ID:         row.ID,
			TaskID:     row.TaskID,
			Relevance:  row.Relevance,
			URL:        row.Url,
			RoutingKey: row.RoutingKey,
			Title:      row.Title,
			Subscribe:  row.Subscribe,
			CreatedAt:  row.CreatedAt,
			Namespace:  row.Namespace,
		})
	}
	return result, nil
}

func toModelLinks(rows []sqlc.TaskLink) []*model.Link {
	links := make([]*model.Link, len(rows))
	for i, row := range rows {
		links[i] = &model.Link{
			ID:         row.ID,
			TaskID:     row.TaskID,
			Relevance:  row.Relevance,
			URL:        row.Url,
			RoutingKey: row.RoutingKey,
			Title:      row.Title,
			Subscribe:  row.Subscribe,
			CreatedAt:  row.CreatedAt,
		}
	}
	return links
}

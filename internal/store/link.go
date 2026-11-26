package store

import (
	"context"
	"database/sql"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store/sqlc"
)

func (s *Store) CreateLink(ctx context.Context, tx *sql.Tx, link *model.Link) error {
	id, err := s.q(tx).CreateLink(ctx, sqlc.CreateLinkParams{
		TaskID:    link.TaskID,
		Relevance: link.Relevance,
		Url:       link.URL,
		Title:     link.Title,
		Kind:      link.Kind,
		Claim:     link.Claim,
		CreatedAt: link.CreatedAt,
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
	links := make([]*model.Link, len(rows))
	for i, row := range rows {
		links[i] = toModelLink(row)
	}
	return links, nil
}

func (s *Store) DeleteLink(ctx context.Context, tx *sql.Tx, id int64) error {
	return s.q(tx).DeleteLink(ctx, id)
}

// SubscribedLink is a link with a kind, with its task's org and agent.
type SubscribedLink struct {
	Link    *model.Link
	OrgID   int64
	AgentID int64
}

// FindSubscribedLinks returns the links with a kind on url of the unarchived
// tasks in orgIDs, ordered by org, task and link.
func (s *Store) FindSubscribedLinks(ctx context.Context, tx *sql.Tx, url string, orgIDs []int64) ([]SubscribedLink, error) {
	rows, err := s.q(tx).FindSubscribedLinks(ctx, sqlc.FindSubscribedLinksParams{
		Url:    url,
		OrgIds: orgIDs,
	})
	if err != nil {
		return nil, err
	}
	var links []SubscribedLink
	for _, row := range rows {
		links = append(links, SubscribedLink{
			Link: toModelLink(sqlc.TaskLink{
				ID:        row.ID,
				TaskID:    row.TaskID,
				Relevance: row.Relevance,
				Url:       row.Url,
				Title:     row.Title,
				CreatedAt: row.CreatedAt,
				Kind:      row.Kind,
				Claim:     row.Claim,
			}),
			OrgID:   row.OrgID,
			AgentID: row.AgentID,
		})
	}
	return links, nil
}

// FindClaimHolders returns the IDs of the unarchived tasks in orgID holding
// one of claims on url: those with a subscribed link to url whose claim is one
// of claims, in ID order.
func (s *Store) FindClaimHolders(ctx context.Context, tx *sql.Tx, orgID int64, url string, claims []string) ([]int64, error) {
	return s.q(tx).FindClaimHolders(ctx, sqlc.FindClaimHoldersParams{
		Url:    url,
		OrgID:  orgID,
		Claims: claims,
	})
}

func toModelLink(row sqlc.TaskLink) *model.Link {
	return &model.Link{
		ID:        row.ID,
		TaskID:    row.TaskID,
		Relevance: row.Relevance,
		URL:       row.Url,
		Title:     row.Title,
		CreatedAt: row.CreatedAt,
		Kind:      row.Kind,
		Claim:     row.Claim,
	}
}

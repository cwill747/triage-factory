package postgres

import (
	"context"

	"github.com/sky-ai-eng/triage-factory/internal/db"
)

type vikunjaProjectStore struct {
	q     queryer
	admin queryer
}

func newVikunjaProjectStore(q, admin queryer) db.VikunjaProjectStore {
	return &vikunjaProjectStore{q: q, admin: admin}
}

var _ db.VikunjaProjectStore = (*vikunjaProjectStore)(nil)

func (s *vikunjaProjectStore) Replace(ctx context.Context, orgID, scope string, routes []db.VikunjaProjectRoute) error {

	return inTx(ctx, s.q, func(q queryer) error {
		if _, err := q.ExecContext(ctx, `DELETE FROM vikunja_project_routes WHERE org_id = $1`, orgID); err != nil {
			return err
		}
		for _, route := range routes {
			if _, err := q.ExecContext(ctx, `INSERT INTO vikunja_project_routes (org_id, scope, project_id, team_id)
                VALUES ($1, $2, $3, $4)`, orgID, scope, route.ProjectID, route.TeamID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *vikunjaProjectStore) List(ctx context.Context, orgID, scope string) ([]db.VikunjaProjectRoute, error) {
	return listVikunjaProjects(ctx, s.q, orgID, scope)
}

func (s *vikunjaProjectStore) ListSystem(ctx context.Context, orgID, scope string) ([]db.VikunjaProjectRoute, error) {
	return listVikunjaProjects(ctx, s.admin, orgID, scope)
}

func listVikunjaProjects(ctx context.Context, q queryer, orgID, scope string) ([]db.VikunjaProjectRoute, error) {

	rows, err := q.QueryContext(ctx, `SELECT project_id, team_id FROM vikunja_project_routes
        WHERE org_id = $1 AND scope = $2 ORDER BY project_id`, orgID, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []db.VikunjaProjectRoute{}
	for rows.Next() {
		var route db.VikunjaProjectRoute
		if err := rows.Scan(&route.ProjectID, &route.TeamID); err != nil {
			return nil, err
		}
		out = append(out, route)
	}
	return out, rows.Err()
}

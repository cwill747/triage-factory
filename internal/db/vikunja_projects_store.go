package db

import "context"

// VikunjaProjectRoute assigns one installation's project to a Triage Factory team.
type VikunjaProjectRoute struct {
	ProjectID int64  `json:"project_id"`
	TeamID    string `json:"team_id"`
}

// VikunjaProjectStore owns the org's project routing configuration. Credentials
// remain in SecretStore; project routes are relational so tracked-set reads can
// enforce team visibility without decrypting credentials.
type VikunjaProjectStore interface {
	// Replace atomically replaces the org's routes, including routes for another
	// installation. An empty set disconnects tracking. This is a bulk write and
	// returns no row. Request callers compose it with the credential write in Tx.
	Replace(ctx context.Context, orgID, scope string, routes []VikunjaProjectRoute) error
	// List reads configuration through the request pool. Org admins can read
	// all routes; other callers see only their teams under RLS.
	List(ctx context.Context, orgID, scope string) ([]VikunjaProjectRoute, error)
	// ListSystem reads the whole configuration for polling, credential sealing,
	// routing, or an explicitly org-admin-gated configuration read. It is not a
	// viewer's tracked-set read: the factory uses the table under team-scoped RLS.
	ListSystem(ctx context.Context, orgID, scope string) ([]VikunjaProjectRoute, error)
}

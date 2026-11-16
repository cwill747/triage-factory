package sqlite_test

import (
	"github.com/sky-ai-eng/triage-factory/internal/db"
	"github.com/sky-ai-eng/triage-factory/internal/db/dbtest"
	sqlitestore "github.com/sky-ai-eng/triage-factory/internal/db/sqlite"
	"github.com/sky-ai-eng/triage-factory/internal/runmode"
	"testing"
)

func TestVikunjaProjectStore_SQLite_Conformance(t *testing.T) {
	dbtest.RunVikunjaProjectStoreConformance(t, func(t *testing.T) (db.Stores, string, string, string) {
		conn := newSQLiteForDeliverableTest(t)
		if _, err := conn.Exec(`INSERT INTO teams (id, org_id, slug, name) VALUES ('vikunja-team-b', ?, 'vikunja-b', 'B')`, runmode.LocalDefaultOrgID); err != nil {
			t.Fatal(err)
		}
		return sqlitestore.New(conn), runmode.LocalDefaultOrgID, runmode.LocalDefaultTeamID, "vikunja-team-b"
	})
}

package postgres_test

import (
	"testing"

	"github.com/sky-ai-eng/triage-factory/internal/db"
	"github.com/sky-ai-eng/triage-factory/internal/db/dbtest"
	"github.com/sky-ai-eng/triage-factory/internal/db/pgtest"
	pgstore "github.com/sky-ai-eng/triage-factory/internal/db/postgres"
)

func TestVikunjaProjectStore_Postgres_Conformance(t *testing.T) {
	dbtest.RunVikunjaProjectStoreConformance(t, func(t *testing.T) (db.Stores, string, string, string) {
		h := pgtest.Shared(t)
		h.Reset(t)
		org, _, teamA := pgtest.SeedOrgWithUser(t, h, "vikunja-routes")
		teamB := pgtest.SeedTeam(t, h, org, "vikunja-b")
		return pgstore.New(h.AdminDB, h.AdminDB, pgtest.SecretKey), org, teamA, teamB
	})
}

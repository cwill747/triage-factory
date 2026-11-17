package postgres_test

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/sky-ai-eng/triage-factory/internal/db"
	"github.com/sky-ai-eng/triage-factory/internal/db/pgtest"
	pgstore "github.com/sky-ai-eng/triage-factory/internal/db/postgres"
	"github.com/sky-ai-eng/triage-factory/internal/domain"
)

func TestFactoryReadStore_Postgres_VikunjaTrackingAndRLS(t *testing.T) {
	h := pgtest.Shared(t)
	h.Reset(t)
	org, alice := seedPgFactoryOrg(t, h)
	teamA := firstTeamForOrg(t, h, org)
	bob := seedPgMember(t, h, org, "vikunja-bob", "member")
	teamB := seedPgDefaultTeam(t, h, org, bob)
	otherOrg, otherUser, _ := pgtest.SeedOrgWithUser(t, h, "vikunja-other")
	stores := pgstore.New(h.AdminDB, h.AdminDB, pgtest.SecretKey)
	ctx := t.Context()
	const scope = "https://tasks.example"
	routes := []db.VikunjaProjectRoute{{ProjectID: 7, TeamID: teamA}, {ProjectID: 8, TeamID: teamB}}
	// An org admin can configure a route for a team they are not a member of.
	if err := h.WithUser(t, alice, org, func(tx *sql.Tx) error {
		s := pgstore.NewForTx(tx, pgtest.SecretKey).VikunjaProjects
		if err := s.Replace(ctx, org, scope, routes); err != nil {
			return err
		}
		got, err := s.List(ctx, org, scope)
		if err == nil && len(got) != 2 {
			return fmt.Errorf("admin configuration read = %v", got)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.WithUser(t, bob, org, func(tx *sql.Tx) error {
		return pgstore.NewForTx(tx, pgtest.SecretKey).VikunjaProjects.Replace(ctx, org, scope, routes)
	}); err == nil {
		t.Fatal("non-admin replaced org-wide project routes")
	}
	if err := h.WithUser(t, otherUser, otherOrg, func(tx *sql.Tx) error {
		got, err := pgstore.NewForTx(tx, pgtest.SecretKey).VikunjaProjects.List(ctx, org, scope)
		if err == nil && len(got) != 0 {
			return fmt.Errorf("cross-org route leak: %v", got)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := stores.VikunjaProjects.Replace(ctx, org, scope, []db.VikunjaProjectRoute{{ProjectID: 7, TeamID: firstTeamForOrg(t, h, otherOrg)}}); err == nil {
		t.Fatal("cross-org team route accepted")
	}
	seedEntity := func(site, id string, project int64) *domain.Entity {
		t.Helper()
		e, _, err := stores.Entities.FindOrCreateSystem(ctx, org, "vikunja", site, id, id, "task", id, "")
		if err != nil {
			t.Fatal(err)
		}
		if project != 0 {
			won, err := stores.Entities.UpdateSnapshotCASSystem(ctx, org, e.ID, fmt.Sprintf(`{"project_id":%d,"done":false}`, project), e.PollSeq)
			if err != nil || !won {
				t.Fatalf("seed snapshot = %t, %v", won, err)
			}
		}
		e, err = stores.Entities.GetSystem(ctx, org, e.ID)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	a := seedEntity(scope, "42", 7)
	b := seedEntity(scope, "43", 8)
	wrongSite := seedEntity("https://other-tasks.example", "42", 7)
	untracked := seedEntity(scope, "44", 9)
	stub := seedEntity(scope, "45", 0)
	seeder := newPgFactorySeeder(h.AdminDB, org, alice, seedPgFactoryPrompt(t, h, org, alice))
	for _, entity := range []*domain.Entity{a, b, wrongSite, untracked, stub} {
		eventID := seeder.Event(t, entity.ID, domain.EventVikunjaTaskAvailable, "", time.Now().UTC(), time.Time{})
		taskID := seeder.Task(t, entity.ID, domain.EventVikunjaTaskAvailable, "", eventID, "queued", time.Now().UTC())
		team := teamA
		if entity.ID == b.ID {
			team = teamB
		}
		pgtest.MustExec(t, h.AdminDB, `UPDATE tasks SET team_id = $1 WHERE id = $2`, team, taskID)
		pgtest.MustExec(t, h.AdminDB, `INSERT INTO task_teams (task_id, team_id) VALUES ($1, $2)`, taskID, team)
	}
	read := func(user, tenant string, teamIDs []string) {
		t.Helper()
		err := h.WithUser(t, user, tenant, func(tx *sql.Tx) error {
			factory := pgstore.NewForTx(tx, pgtest.SecretKey).Factory
			// Upstream's factory belt now carries pull requests only.
			// Vikunja remains scoped through the shared tracked-set reads.
			rows, err := factory.Entities(ctx, tenant, 100, teamIDs)
			if err != nil {
				return err
			}
			got := map[string]bool{}
			for _, row := range rows {
				got[row.Entity.ID] = true
			}
			if len(got) != 0 {
				return fmt.Errorf("PR-only belt includes Vikunja tasks: %v", got)
			}

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	read(alice, org, nil)
	read(bob, org, nil)
	read(alice, org, []string{teamA})
	read(alice, org, []string{teamB})
	read(bob, org, []string{teamB})
	read(otherUser, otherOrg, nil)
	// Counters have the same population as the belt, including for the org
	// admin who can configure (but is not a member of) the other team.
	for _, user := range []string{alice, bob} {
		if err := h.WithUser(t, user, org, func(tx *sql.Tx) error {
			factory := pgstore.NewForTx(tx, pgtest.SecretKey).Factory
			events, err := factory.EventCountsSince(ctx, org, time.Now().Add(-time.Hour))
			if err != nil {
				return err
			}
			lifetime, err := factory.LifetimeDistinctByEventType(ctx, org)
			if err != nil {
				return err
			}
			tasks, err := factory.TaskCountsSince(ctx, org, time.Now().Add(-time.Hour))
			if err != nil {
				return err
			}
			if events[domain.EventVikunjaTaskAvailable] != 0 || lifetime[domain.EventVikunjaTaskAvailable] != 0 || tasks[domain.EventVikunjaTaskAvailable] != 0 {
				return fmt.Errorf("counters leaked or omitted work: events=%v lifetime=%v tasks=%v", events, lifetime, tasks)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := stores.Entities.ClearSnapshotsForSourceSystem(ctx, org, "vikunja"); err != nil {
		t.Fatal(err)
	}
	read(alice, org, nil)
	read(bob, org, nil)
	// A project move changes ownership in the same CAS that writes the snapshot.
	a, _ = stores.Entities.GetSystem(ctx, org, a.ID)
	if won, err := stores.Entities.UpdateSnapshotCASSystem(ctx, org, a.ID, `{"project_id":8,"done":false}`, a.PollSeq); err != nil || !won {
		t.Fatalf("move = %t, %v", won, err)
	}
	read(alice, org, nil)
	read(bob, org, nil)
	if err := stores.VikunjaProjects.Replace(ctx, org, scope, routes[:1]); err != nil {
		t.Fatal(err)
	}
	read(bob, org, nil)
	// Untracking affects reads, never the durable work created earlier.
	if tasks, err := stores.Tasks.FindActiveByEntityAndTypeSystem(ctx, org, a.ID, domain.EventVikunjaTaskAvailable); err != nil || len(tasks) != 1 {
		t.Fatalf("untracking removed tasks: %v, %v", tasks, err)
	}
	if err := stores.VikunjaProjects.Replace(ctx, org, "", nil); err != nil {
		t.Fatal(err)
	}
	read(alice, org, nil)
}

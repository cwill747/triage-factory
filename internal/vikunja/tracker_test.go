package vikunja

import (
	"context"
	"database/sql"
	"testing"

	"github.com/sky-ai-eng/triage-factory/internal/db"
	sqlitestore "github.com/sky-ai-eng/triage-factory/internal/db/sqlite"
	"github.com/sky-ai-eng/triage-factory/internal/domain"
	"github.com/sky-ai-eng/triage-factory/internal/runmode"
	"github.com/sky-ai-eng/triage-factory/internal/server"
	_ "modernc.org/sqlite"
)

type trackerAPI struct {
	server.ExtensionAPI
	stores    db.Stores
	published []domain.Event
}

func (a *trackerAPI) Stores() db.Stores { return a.stores }
func (a *trackerAPI) PublishPreEnqueued(_ context.Context, e domain.Event) {
	a.published = append(a.published, e)
}

func TestTrackerDurableLifecycle(t *testing.T) {
	conn, err := sql.Open("sqlite", db.TestDSNMemory)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	if err := db.BootstrapSchemaForTest(conn); err != nil {
		t.Fatal(err)
	}
	api := &trackerAPI{stores: sqlitestore.New(conn)}
	h := &integration{api: api}
	org := runmode.LocalDefaultOrgID
	c := Connection{BaseURL: "https://tasks.example"}
	task := Task{ID: 42, ProjectID: 7, Title: "first", Description: "body"}
	observe := func() {
		t.Helper()
		if err := h.observe(t.Context(), org, c, task); err != nil {
			t.Fatal(err)
		}
	}
	observe()
	observe()
	if len(api.published) != 1 || api.published[0].EventType != domain.EventVikunjaTaskAvailable {
		t.Fatalf("discovery=%+v", api.published)
	}
	var durable int
	if err := conn.QueryRow(`SELECT count(*) FROM event_queue WHERE entity_id = ?`, *api.published[0].EntityID).Scan(&durable); err != nil || durable != 1 {
		t.Fatalf("durable=%d err=%v", durable, err)
	}
	task.Description = "edited"
	observe()
	if len(api.published) != 2 || api.published[1].EventType != domain.EventVikunjaTaskUpdated {
		t.Fatalf("update=%+v", api.published)
	}
	task.Done = true
	observe()
	if len(api.published) != 3 || api.published[2].EventType != domain.EventVikunjaTaskCompleted {
		t.Fatalf("completion=%+v", api.published)
	}
	// Simulate settlement by the router, then rediscover the reopened entity.
	entity, err := api.stores.Entities.GetBySourceSystem(t.Context(), org, "vikunja", c.BaseURL, "42")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.stores.Entities.CloseWithSnapshotCASSystem(t.Context(), org, entity.ID, entity.SnapshotJSON, entity.PollSeq); err != nil {
		t.Fatal(err)
	}
	task.Done = false
	observe()
	entity, err = api.stores.Entities.GetBySourceSystem(t.Context(), org, "vikunja", c.BaseURL, "42")
	if err != nil || entity.State != "active" || len(api.published) != 3 {
		t.Fatalf("reactivation=%+v events=%d err=%v", entity, len(api.published), err)
	}
	task.ID, task.Done = 43, true
	observe()
	if len(api.published) != 3 {
		t.Fatal("initial done task produced historical work")
	}
	// Disabled sources must neither emit nor create entity rows.
	if _, err := api.stores.OrgEventSources.SetDisabled(t.Context(), org, "vikunja", true, ""); err != nil {
		t.Fatal(err)
	}
	task.ID, task.Done = 44, false
	observe()
	entity, err = api.stores.Entities.GetBySourceSystem(t.Context(), org, "vikunja", c.BaseURL, "44")
	if err != nil || entity != nil || len(api.published) != 3 {
		t.Fatalf("paused entity=%+v err=%v", entity, err)
	}
}

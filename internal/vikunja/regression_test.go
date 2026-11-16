package vikunja

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/sky-ai-eng/triage-factory/internal/db"
	sqlitestore "github.com/sky-ai-eng/triage-factory/internal/db/sqlite"
	"github.com/sky-ai-eng/triage-factory/internal/domain"
	"github.com/sky-ai-eng/triage-factory/internal/runmode"
)

func regressionTracker(t *testing.T) (*integration, *trackerAPI) {
	t.Helper()
	conn, err := sql.Open("sqlite", db.TestDSNMemory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	if err := db.BootstrapSchemaForTest(conn); err != nil {
		t.Fatal(err)
	}
	api := &trackerAPI{stores: sqlitestore.New(conn)}
	return &integration{api: api}, api
}

func TestTrackerPauseReseedsExistingEntities(t *testing.T) {
	for _, done := range []bool{false, true} {
		t.Run(fmt.Sprintf("done=%t", done), func(t *testing.T) {
			h, api := regressionTracker(t)
			org := runmode.LocalDefaultOrgID
			c := Connection{BaseURL: "https://tasks.example"}
			task := Task{ID: 42, ProjectID: 7, Title: "before pause"}
			observe := func() {
				t.Helper()
				if err := h.observe(t.Context(), org, c, task); err != nil {
					t.Fatal(err)
				}
			}
			observe()
			if len(api.published) != 1 {
				t.Fatalf("discovery = %v", api.published)
			}
			if _, err := api.stores.OrgEventSources.SetDisabled(t.Context(), org, "vikunja", true, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := api.stores.Entities.ClearSnapshotsForSourceSystem(t.Context(), org, "vikunja"); err != nil {
				t.Fatal(err)
			}
			if _, err := api.stores.OrgEventSources.SetDisabled(t.Context(), org, "vikunja", false, ""); err != nil {
				t.Fatal(err)
			}
			task.Title, task.Done = "during pause", done
			observe()
			if len(api.published) != 1 {
				t.Fatalf("resuming replayed work: %v", api.published)
			}
			entity, err := api.stores.Entities.GetBySourceSystem(t.Context(), org, "vikunja", c.BaseURL, "42")
			if err != nil || entity == nil || entity.SnapshotJSON == "" || entity.VikunjaProjectID != 7 {
				t.Fatalf("reseed = %+v, %v", entity, err)
			}
			if !done {
				task.Title = "after pause"
				observe()
				if len(api.published) != 2 || api.published[1].EventType != domain.EventVikunjaTaskUpdated {
					t.Fatalf("future change = %v", api.published)
				}
			}
		})
	}
}

func TestPollContinuesAfterRetainedTaskReadFails(t *testing.T) {
	h, api := regressionTracker(t)
	org := runmode.LocalDefaultOrgID
	requests := 0
	var failedID string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		id := strings.TrimPrefix(r.URL.Path, "/api/v2/tasks/")
		if requests == 1 {
			failedID = id
			w.WriteHeader(http.StatusNotFound)
			return
		}
		n, _ := strconv.ParseInt(id, 10, 64)
		_ = json.NewEncoder(w).Encode(Task{ID: n, ProjectID: 7, Title: "retained", Done: true})
	}))
	defer upstream.Close()
	c := Connection{BaseURL: upstream.URL, Token: "secret", PollInterval: 60}
	for id := int64(1); id <= 3; id++ {
		if err := h.observe(t.Context(), org, c, Task{ID: id, ProjectID: 7, Title: "retained"}); err != nil {
			t.Fatal(err)
		}
	}
	api.published = nil
	raw, _ := json.Marshal(c)
	api.stores.Secrets = execSecrets{value: string(raw)}
	interval, err := h.pollOrg(t.Context(), org)
	if err == nil || !strings.Contains(err.Error(), "404") || interval != 60 {
		t.Fatalf("interval=%d err=%v", interval, err)
	}
	if requests != 3 || len(api.published) != 2 {
		t.Fatalf("requests=%d events=%v", requests, api.published)
	}
	for _, event := range api.published {
		if event.EventType != domain.EventVikunjaTaskCompleted {
			t.Fatalf("event=%+v", event)
		}
	}
	failed, err := api.stores.Entities.GetBySourceSystem(t.Context(), org, "vikunja", c.BaseURL, failedID)
	if err != nil || failed == nil || failed.State != "active" {
		t.Fatalf("failed read inferred completion: %+v, %v", failed, err)
	}
}

func TestPollContinuesAfterProjectReadFails(t *testing.T) {
	h, api := regressionTracker(t)
	org := runmode.LocalDefaultOrgID
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/projects/7/tasks" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(pageResult[Task]{Items: []Task{{ID: 42, ProjectID: 8, Title: "healthy project"}}, Page: 1, PerPage: 100, Total: 1, TotalPages: 1})
	}))
	defer upstream.Close()
	c := Connection{BaseURL: upstream.URL, Token: "secret", PollInterval: 60}
	raw, _ := json.Marshal(c)
	api.stores.Secrets = execSecrets{value: string(raw)}
	if err := api.stores.VikunjaProjects.Replace(t.Context(), org, c.BaseURL, []ProjectRoute{{ProjectID: 7, TeamID: runmode.LocalDefaultTeamID}, {ProjectID: 8, TeamID: runmode.LocalDefaultTeamID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pollOrg(t.Context(), org); err == nil {
		t.Fatal("lost project failure")
	}
	if len(api.published) != 1 || api.published[0].EventType != domain.EventVikunjaTaskAvailable {
		t.Fatalf("healthy project was skipped: %v", api.published)
	}
}

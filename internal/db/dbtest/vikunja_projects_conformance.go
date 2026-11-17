package dbtest

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/sky-ai-eng/triage-factory/internal/db"
	"github.com/sky-ai-eng/triage-factory/internal/domain"
)

// RunVikunjaProjectStoreConformance exercises route replacement and the entity's
// persisted project identity through both dialects' real snapshot CAS writes.
func RunVikunjaProjectStoreConformance(t *testing.T, mk func(t *testing.T) (db.Stores, string, string, string)) {
	t.Helper()
	stores, org, teamA, teamB := mk(t)
	ctx := t.Context()
	const scope = "https://tasks.example"
	want := []db.VikunjaProjectRoute{{ProjectID: 7, TeamID: teamA}, {ProjectID: 8, TeamID: teamB}}
	replace := func(scope string, routes []db.VikunjaProjectRoute) {
		t.Helper()
		if err := stores.VikunjaProjects.Replace(ctx, org, scope, routes); err != nil {
			t.Fatal(err)
		}
	}
	read := func(scope string, want []db.VikunjaProjectRoute) {
		t.Helper()
		got, err := stores.VikunjaProjects.List(ctx, org, scope)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("List = %+v, %v; want %+v", got, err, want)
		}
		system, err := stores.VikunjaProjects.ListSystem(ctx, org, scope)
		if err != nil || !reflect.DeepEqual(system, want) {
			t.Fatalf("ListSystem = %+v, %v; want %+v", system, err, want)
		}
	}
	replace(scope, want)
	read(scope, want)
	read("https://other.example", []db.VikunjaProjectRoute{})
	if err := stores.VikunjaProjects.Replace(ctx, org, scope, []db.VikunjaProjectRoute{want[0], want[0]}); err == nil {
		t.Fatal("duplicate project accepted")
	}
	read(scope, want)
	if err := stores.VikunjaProjects.Replace(ctx, org, scope, []db.VikunjaProjectRoute{{ProjectID: 0, TeamID: teamA}}); err == nil {
		t.Fatal("zero project accepted")
	}
	read(scope, want)
	if err := stores.VikunjaProjects.Replace(ctx, org, "", want); err == nil {
		t.Fatal("empty installation accepted")
	}
	read(scope, want)
	replace(scope, want[1:])
	read(scope, want[1:])
	replace("https://new.example", want)
	read(scope, []db.VikunjaProjectRoute{})
	read("https://new.example", want)
	replace("", nil)
	read("https://new.example", []db.VikunjaProjectRoute{})

	entity, _, err := stores.Entities.FindOrCreateSystem(ctx, org, "vikunja", scope, "42", "42", "task", "task", "")
	if err != nil {
		t.Fatal(err)
	}
	write := func(project int64) {
		t.Helper()
		snapshot, _ := json.Marshal(map[string]any{"project_id": project, "done": false})
		won, _, err := stores.EventQueue.EnqueueBatchWithSnapshotCAS(ctx, org, entity.ID, string(snapshot), entity.PollSeq, nil, nil)
		if err != nil || !won {
			t.Fatalf("snapshot CAS = %t, %v", won, err)
		}
		entity, err = stores.Entities.GetSystem(ctx, org, entity.ID)
		if err != nil || entity == nil || entity.VikunjaProjectID != project {
			t.Fatalf("project = %+v, %v", entity, err)
		}
	}
	write(7)
	if won, err := stores.Entities.UpdateSnapshotCASSystem(ctx, org, entity.ID, `{"project_id":99}`, entity.PollSeq-1); err != nil || won {
		t.Fatalf("stale snapshot CAS = %t, %v", won, err)
	}
	write(8)
	if _, err := stores.Entities.ClearSnapshotsForSourceSystem(ctx, org, "vikunja"); err != nil {
		t.Fatal(err)
	}
	entity, err = stores.Entities.GetSystem(ctx, org, entity.ID)
	if err != nil || entity == nil || entity.SnapshotJSON != "" || entity.VikunjaProjectID != 8 {
		t.Fatalf("pause lost project: %+v, %v", entity, err)
	}
	snapshot := `{"project_id":9,"done":true}`
	won, err := stores.Entities.CloseWithSnapshotCASSystem(ctx, org, entity.ID, snapshot, entity.PollSeq)
	if err != nil || !won {
		t.Fatalf("close CAS = %t, %v", won, err)
	}
	entity, err = stores.Entities.GetSystem(ctx, org, entity.ID)
	if err != nil || entity.VikunjaProjectID != 9 {
		t.Fatalf("closed project = %+v, %v", entity, err)
	}
	won, err = stores.Entities.ReactivateWithSnapshotCASSystem(ctx, org, entity.ID, `{"project_id":10,"done":false}`, entity.PollSeq)
	if err != nil || !won {
		t.Fatalf("reactivate CAS = %t, %v", won, err)
	}
	entity, err = stores.Entities.GetSystem(ctx, org, entity.ID)
	if err != nil || entity.VikunjaProjectID != 10 {
		t.Fatalf("reactivated project = %+v, %v", entity, err)
	}

	replace(scope, []db.VikunjaProjectRoute{{ProjectID: 10, TeamID: teamA}})
	if _, err := stores.EventQueue.Enqueue(ctx, org, domain.Event{OrgID: org, EntityID: &entity.ID, EventType: domain.EventVikunjaTaskAvailable, MetadataJSON: `{}`}, ""); err != nil {
		t.Fatal(err)
	}
	activity, err := stores.TeamActivity.TeamActivity(ctx, org, teamA, time.Now().Add(-time.Hour), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	foundSource, foundProject := false, false
	for _, source := range activity.BySource {
		if source.Source == "vikunja" {
			foundSource = source.Events != nil && *source.Events == 1
		}
	}
	for _, unit := range activity.ByTracked {
		if unit.Source == "vikunja" && unit.UnitID == scope+"/projects/10" {
			foundProject = unit.Events == 1 && unit.LastEventAt != nil
		}
	}
	if !foundSource || !foundProject {
		t.Fatalf("Vikunja activity omitted or disagreed: %+v", activity)
	}
}

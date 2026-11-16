package vikunja

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sky-ai-eng/triage-factory/internal/db"
	"github.com/sky-ai-eng/triage-factory/internal/domain"
	"github.com/sky-ai-eng/triage-factory/internal/domain/events"
	"github.com/sky-ai-eng/triage-factory/internal/routing"
	"github.com/sky-ai-eng/triage-factory/internal/runmode"
	"github.com/sky-ai-eng/triage-factory/pkg/websocket"
)

type failFirstAdmission struct {
	db.EventQueueStore
	failure error
}

func (s *failFirstAdmission) EnqueueBatchWithSnapshotCAS(ctx context.Context, orgID, entityID, snapshotJSON string, seq int64, batch []domain.Event, traceparents []string) (bool, []string, error) {
	if s.failure != nil {
		err := s.failure
		s.failure = nil
		return false, nil, err
	}
	return s.EventQueueStore.EnqueueBatchWithSnapshotCAS(ctx, orgID, entityID, snapshotJSON, seq, batch, traceparents)
}

func TestTrackerDiscoversUninitializedEntities(t *testing.T) {
	for _, scenario := range []string{"exec stub", "paused stub", "failed discovery admission"} {
		t.Run(scenario, func(t *testing.T) {
			h, api := regressionTracker(t)
			ctx := t.Context()
			org := runmode.LocalDefaultOrgID
			c := Connection{BaseURL: "https://tasks.example"}
			task := Task{ID: 42, ProjectID: 7, Title: "discover me"}
			if scenario != "failed discovery admission" {
				if _, _, err := api.stores.Entities.FindOrCreateSystem(ctx, org, "vikunja", c.BaseURL, "42", "42", "task", "", ""); err != nil {
					t.Fatal(err)
				}
				if scenario == "paused stub" {
					if _, err := api.stores.Entities.ClearSnapshotsForSourceSystem(ctx, org, "vikunja"); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				failure := errors.New("admission unavailable")
				api.stores.EventQueue = &failFirstAdmission{EventQueueStore: api.stores.EventQueue, failure: failure}
				if err := h.observe(ctx, org, c, task); !errors.Is(err, failure) {
					t.Fatalf("first admission = %v, want injected failure", err)
				}
			}
			entity, err := api.stores.Entities.GetBySourceSystem(ctx, org, "vikunja", c.BaseURL, "42")
			if err != nil || entity == nil || entity.SnapshotJSON != "" || entity.VikunjaProjectID != 0 {
				t.Fatalf("uninitialized entity = %+v, %v", entity, err)
			}
			for i := 0; i < 2; i++ {
				if err := h.observe(ctx, org, c, task); err != nil {
					t.Fatal(err)
				}
			}
			if len(api.published) != 1 || api.published[0].EventType != domain.EventVikunjaTaskAvailable {
				t.Fatalf("discovery was lost or duplicated: %+v", api.published)
			}
			queued, err := api.stores.EventQueue.ListForEntity(ctx, org, entity.ID)
			if err != nil || len(queued) != 1 || queued[0].EventType != domain.EventVikunjaTaskAvailable {
				t.Fatalf("durable discovery = %+v, %v", queued, err)
			}
		})
	}
}

type closeOnlyDelegator struct {
	routing.Delegator
	closed []string
}

func (d *closeOnlyDelegator) TellTaskClosed(_ context.Context, _, taskID string, _ []string) {
	d.closed = append(d.closed, taskID)
}

type recoveryScorer struct{}

func (recoveryScorer) Trigger(string) {}

type recoveryPublisher struct{}

func (recoveryPublisher) Publish(domain.Event) {}

func TestTrackerPausedCompletionClosesExistingTasks(t *testing.T) {
	h, api := regressionTracker(t)
	ctx := t.Context()
	org := runmode.LocalDefaultOrgID
	c := Connection{BaseURL: "https://tasks.example"}
	task := Task{ID: 42, ProjectID: 7, Title: "before pause"}
	var taskIDs []string
	for _, title := range []string{"before pause", "changed before pause"} {
		task.Title = title
		if err := h.observe(ctx, org, c, task); err != nil {
			t.Fatal(err)
		}
		evt := api.published[len(api.published)-1]
		work, _, err := api.stores.Tasks.FindOrCreateAtSystem(ctx, org, runmode.LocalDefaultTeamID, *evt.EntityID, evt.EventType, "", evt.ID, 0.5, time.Now().UTC())
		if err != nil || work == nil {
			t.Fatalf("seed task: %+v, %v", work, err)
		}
		taskIDs = append(taskIDs, work.ID)
	}
	if _, err := api.stores.OrgEventSources.SetDisabled(ctx, org, "vikunja", true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := api.stores.Entities.ClearSnapshotsForSourceSystem(ctx, org, "vikunja"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.stores.OrgEventSources.SetDisabled(ctx, org, "vikunja", false, ""); err != nil {
		t.Fatal(err)
	}
	task.Done = true
	if err := h.observe(ctx, org, c, task); err != nil {
		t.Fatal(err)
	}
	if len(api.published) != 3 || api.published[2].EventType != domain.EventSystemEntityCloseOwed {
		t.Fatalf("expected a repair, not a completion automation event: %+v", api.published)
	}
	owed := api.published[2]
	var meta events.SystemEntityCloseOwedMetadata
	if err := json.Unmarshal([]byte(owed.MetadataJSON), &meta); err != nil || meta.Reason != events.SystemEntityCloseOwedReasonTerminalSnapshot {
		t.Fatalf("close obligation metadata = %+v, %v", meta, err)
	}
	entity, err := api.stores.Entities.GetSystem(ctx, org, *owed.EntityID)
	if err != nil || entity == nil || entity.State != "active" {
		t.Fatalf("the poll bypassed the router's close: %+v, %v", entity, err)
	}
	queue, err := api.stores.EventQueue.ListForEntity(ctx, org, entity.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range queue {
		if row.EventType == domain.EventSystemEntityCloseOwed {
			found = row.EntityPollSeq != nil && *row.EntityPollSeq == entity.PollSeq
		}
	}
	if !found {
		t.Fatal("the close obligation was not durably fenced at the seeded snapshot")
	}
	s := api.stores
	delegator := &closeOnlyDelegator{}
	router, err := routing.NewRouter(routing.RouterDeps{
		Handlers: s.EventHandlers, Tasks: s.Tasks, Conversations: s.Conversations,
		Entities: s.Entities, Firings: s.PendingFirings, Events: s.Events,
		EventQueue: s.EventQueue, Orgs: s.Orgs, Teams: s.Teams,
		TeamRepos: s.TeamGitHubRepos, JiraRules: s.JiraStatusRules,
		LinearRules: s.LinearTeamRules, ReDerive: s.TaskReDerive,
		Mergeability: s.PRMergeability, EventSources: s.OrgEventSources,
		Spawner: delegator, Scorer: recoveryScorer{}, WS: websocket.NewHub(),
		Publisher: recoveryPublisher{}, ExecutorID: "vikunja-recovery-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := router.HandleEvent(ctx, owed); err != nil {
		t.Fatal(err)
	}
	for _, id := range taskIDs {
		work, err := s.Tasks.GetSystem(ctx, org, id)
		if err != nil || work == nil || work.Status != "done" || work.CloseReason != "reconciled" {
			t.Fatalf("repair did not close task %s: %+v, %v", id, work, err)
		}
	}
	if len(delegator.closed) != len(taskIDs) {
		t.Fatalf("close notifications = %v, want one per task", delegator.closed)
	}
	entity, err = s.Entities.GetSystem(ctx, org, entity.ID)
	if err != nil || entity == nil || entity.State != "closed" {
		t.Fatalf("repaired entity = %+v, %v", entity, err)
	}
	if err := h.observe(ctx, org, c, task); err != nil {
		t.Fatal(err)
	}
	if len(api.published) != 3 {
		t.Fatal("settled completion emitted another event")
	}
}

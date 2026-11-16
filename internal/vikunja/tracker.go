package vikunja

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/sky-ai-eng/triage-factory/internal/domain"
	"github.com/sky-ai-eng/triage-factory/internal/domain/events"
	"github.com/sky-ai-eng/triage-factory/internal/eventing/eventsource"
)

type snapshot struct {
	ProjectID int64  `json:"project_id"`
	Title     string `json:"title"`
	BodyHash  string `json:"body_hash"`
	Priority  int    `json:"priority"`
	Done      bool   `json:"done"`
}

func taskSnapshot(t Task) snapshot {
	hash := sha256.Sum256([]byte(t.Description))
	return snapshot{ProjectID: t.ProjectID, Title: t.Title, BodyHash: hex.EncodeToString(hash[:]), Priority: t.Priority, Done: t.Done}
}

func transition(previous *snapshot, current snapshot) string {
	if previous == nil {
		if current.Done {
			return ""
		}
		return domain.EventVikunjaTaskAvailable
	}
	if previous.Done != current.Done {
		if current.Done {
			return domain.EventVikunjaTaskCompleted
		}
		return domain.EventVikunjaTaskAvailable
	}
	if !current.Done && *previous != current {
		return domain.EventVikunjaTaskUpdated
	}
	return ""
}

func (h *integration) observe(ctx context.Context, org string, c Connection, task Task) error {
	s := h.api.Stores()
	if task.ID <= 0 || task.ProjectID <= 0 {
		return fmt.Errorf("invalid Vikunja task identity")
	}
	// Check again at the write boundary, not just before the network scan.
	off, err := eventsource.Disabled(ctx, s.OrgEventSources, org, "vikunja")
	if err != nil || off {
		return err
	}
	id := strconv.FormatInt(task.ID, 10)
	entity, created, err := s.Entities.FindOrCreateSystem(ctx, org, "vikunja", c.BaseURL, id, id, "task", task.Title, c.BaseURL+"/tasks/"+id)
	if err != nil {
		return err
	}
	current := taskSnapshot(task)
	encoded, err := json.Marshal(current)
	if err != nil {
		return err
	}
	var previous *snapshot
	if entity.SnapshotJSON != "" {
		previous = &snapshot{}
		if err := json.Unmarshal([]byte(entity.SnapshotJSON), previous); err != nil {
			return fmt.Errorf("invalid Vikunja task snapshot: %w", err)
		}
	}
	// Initial state is a quiet baseline, as on the existing issue trackers:
	// discovery of an already-done task must not create historical work.
	if previous == nil && current.Done {
		_, err := s.Entities.CloseWithSnapshotCASSystem(ctx, org, entity.ID, string(encoded), entity.PollSeq)
		return err
	}
	if entity.State == "closed" {
		if current.Done {
			return nil
		}
		won, err := s.Entities.ReactivateWithSnapshotCASSystem(ctx, org, entity.ID, string(encoded), entity.PollSeq)
		if err != nil || !won {
			return err
		}
		// Reactivation is intentionally quiet; future transitions route normally.
		return nil
	}
	et := transition(previous, current)
	// A cleared snapshot belongs to an existing entity, not new discovery.
	// Seed it quietly so resuming a paused source cannot replay old work.
	if previous == nil && !created {
		et = ""
	}
	if et == "" && current.Done {
		// Retry a lost close via the shared durable close obligation. The outbox
		// coalesces it while an earlier terminal transition is still unsettled.
		et = domain.EventSystemEntityCloseOwed
	}
	var batch []domain.Event
	if et != "" {
		meta, err := json.Marshal(events.VikunjaTaskMetadata{TaskID: task.ID, ProjectID: task.ProjectID, Title: task.Title, Done: task.Done, Priority: task.Priority})
		if err != nil {
			return err
		}
		batch = []domain.Event{{OrgID: org, EntityID: &entity.ID, EventType: et, MetadataJSON: string(meta)}}
	}
	won, ids, err := s.EventQueue.EnqueueBatchWithSnapshotCAS(ctx, org, entity.ID, string(encoded), entity.PollSeq, batch, nil)
	if err != nil || !won {
		return err
	}
	for i := range batch {
		if ids[i] == "" {
			continue
		}
		batch[i].ID = ids[i]
		h.api.PublishPreEnqueued(ctx, batch[i])
	}
	// Display fields are retryable enrichment, not transition consumption.
	if _, err := s.Entities.UpdateTitleSystem(ctx, org, entity.ID, task.Title); err != nil {
		return err
	}
	_, err = s.Entities.UpdateDescriptionSystem(ctx, org, entity.ID, task.Description)
	return err
}

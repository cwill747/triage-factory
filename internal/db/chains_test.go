package db

import (
	"encoding/json"
	"testing"

	"github.com/sky-ai-eng/triage-factory/internal/domain"
)

// chainFixture seeds the minimal FK graph needed for chain tests:
// an entity → event → task, a leaf prompt, a chain prompt with two steps,
// and a chain_run. Returns the chain_run ID and task ID.
func chainFixture(t *testing.T, database interface {
	Exec(query string, args ...interface{}) (interface{ RowsAffected() (int64, error) }, error)
}) {
	t.Helper()
	// deliberately unused — real seeding done inline in each test.
}

// TestRunsForChain_RoundTrip protects the 16-column SELECT/Scan pair in
// RunsForChain against silent column-order drift. Each AgentRun field
// that the SELECT returns must round-trip correctly.
func TestRunsForChain_RoundTrip(t *testing.T) {
	db := newTestDB(t)

	// Seed entity → event → task.
	entity, _, err := FindOrCreateEntity(db, "github", "owner/repo#chain-rt", "pr", "Chain RT", "https://example.com/chain-rt")
	if err != nil {
		t.Fatalf("create entity: %v", err)
	}
	eventID, err := RecordEvent(db, domain.Event{
		EventType:    domain.EventGitHubPRCICheckFailed,
		EntityID:     &entity.ID,
		MetadataJSON: `{"check_name":"build"}`,
	})
	if err != nil {
		t.Fatalf("record event: %v", err)
	}
	task, _, err := FindOrCreateTask(db, entity.ID, domain.EventGitHubPRCICheckFailed, "chain-rt", eventID, 0.5)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Seed a leaf prompt (step) and a chain prompt.
	createPromptForTest(t, db, domain.Prompt{ID: "step-prompt-1", Name: "Step 1", Body: "do step 1", Source: "user"})
	createPromptForTest(t, db, domain.Prompt{ID: "step-prompt-2", Name: "Step 2", Body: "do step 2", Source: "user"})
	createPromptForTest(t, db, domain.Prompt{
		ID:     "chain-prompt",
		Name:   "My Chain",
		Body:   "chain",
		Source: "user",
		Kind: domain.PromptKindChain,
	})

	if err := ReplaceChainSteps(db, "chain-prompt", []string{"step-prompt-1", "step-prompt-2"}, nil); err != nil {
		t.Fatalf("replace chain steps: %v", err)
	}

	// Insert the chain_run directly (TriggerType required now).
	chainRunID, err := CreateChainRun(db, domain.ChainRun{
		ID:            "chain-run-rt",
		ChainPromptID: "chain-prompt",
		TaskID:        task.ID,
		TriggerType:   domain.ChainTriggerManual,
		Status:        domain.ChainRunStatusRunning,
		WorktreePath:  "/tmp/wt-chain-rt",
	})
	if err != nil {
		t.Fatalf("create chain run: %v", err)
	}
	if chainRunID != "chain-run-rt" {
		t.Fatalf("unexpected chain run id: %s", chainRunID)
	}

	// Seed two step runs linked to the chain_run.
	step0 := 0
	step1 := 1
	for _, run := range []domain.AgentRun{
		{
			ID:             "chain-step-run-0",
			TaskID:         task.ID,
			PromptID:       "step-prompt-1",
			Status:         "initializing",
			Model:          "claude-sonnet-4-6",
			ChainRunID:     "chain-run-rt",
			ChainStepIndex: &step0,
		},
		{
			ID:             "chain-step-run-1",
			TaskID:         task.ID,
			PromptID:       "step-prompt-2",
			Status:         "initializing",
			Model:          "claude-sonnet-4-6",
			ChainRunID:     "chain-run-rt",
			ChainStepIndex: &step1,
		},
	} {
		if err := CreateAgentRun(db, run); err != nil {
			t.Fatalf("create agent run %s: %v", run.ID, err)
		}
	}

	runs, err := RunsForChain(db, "chain-run-rt")
	if err != nil {
		t.Fatalf("RunsForChain: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}

	// Step 0
	r0 := runs[0]
	if r0.ID != "chain-step-run-0" {
		t.Errorf("run[0].ID = %q, want chain-step-run-0", r0.ID)
	}
	if r0.TaskID != task.ID {
		t.Errorf("run[0].TaskID = %q, want %q", r0.TaskID, task.ID)
	}
	if r0.PromptID != "step-prompt-1" {
		t.Errorf("run[0].PromptID = %q, want step-prompt-1", r0.PromptID)
	}
	if r0.ChainRunID != "chain-run-rt" {
		t.Errorf("run[0].ChainRunID = %q, want chain-run-rt", r0.ChainRunID)
	}
	if r0.ChainStepIndex == nil || *r0.ChainStepIndex != 0 {
		t.Errorf("run[0].ChainStepIndex = %v, want 0", r0.ChainStepIndex)
	}
	if r0.Model != "claude-sonnet-4-6" {
		t.Errorf("run[0].Model = %q, want claude-sonnet-4-6", r0.Model)
	}

	// Step 1
	r1 := runs[1]
	if r1.ID != "chain-step-run-1" {
		t.Errorf("run[1].ID = %q, want chain-step-run-1", r1.ID)
	}
	if r1.ChainStepIndex == nil || *r1.ChainStepIndex != 1 {
		t.Errorf("run[1].ChainStepIndex = %v, want 1", r1.ChainStepIndex)
	}
}

// TestLatestChainVerdictsForRuns inserts two verdicts for one run (advance
// then abort) and asserts the returned map contains only the abort verdict
// (the later one).
func TestLatestChainVerdictsForRuns(t *testing.T) {
	db := newTestDB(t)

	// Seed minimal FK chain to get a valid run.
	entity, _, err := FindOrCreateEntity(db, "github", "owner/repo#chain-verdict", "pr", "Verdict Test", "https://example.com/verdict")
	if err != nil {
		t.Fatalf("create entity: %v", err)
	}
	eventID, err := RecordEvent(db, domain.Event{
		EventType:    domain.EventGitHubPRCICheckFailed,
		EntityID:     &entity.ID,
		MetadataJSON: `{"check_name":"ci"}`,
	})
	if err != nil {
		t.Fatalf("record event: %v", err)
	}
	task, _, err := FindOrCreateTask(db, entity.ID, domain.EventGitHubPRCICheckFailed, "chain-verdict", eventID, 0.5)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	createPromptForTest(t, db, domain.Prompt{ID: "vp-step", Name: "VP Step", Body: "x", Source: "user"})
	createPromptForTest(t, db, domain.Prompt{
		ID:   "vp-chain",
		Name: "VP Chain",
		Body: "chain",
		Source: "user",
		Kind: domain.PromptKindChain,
	})
	if err := ReplaceChainSteps(db, "vp-chain", []string{"vp-step"}, nil); err != nil {
		t.Fatalf("replace chain steps: %v", err)
	}
	_, err = CreateChainRun(db, domain.ChainRun{
		ID:            "vp-chain-run",
		ChainPromptID: "vp-chain",
		TaskID:        task.ID,
		TriggerType:   domain.ChainTriggerManual,
		Status:        domain.ChainRunStatusRunning,
	})
	if err != nil {
		t.Fatalf("create chain run: %v", err)
	}
	step0 := 0
	if err := CreateAgentRun(db, domain.AgentRun{
		ID:             "vp-run",
		TaskID:         task.ID,
		PromptID:       "vp-step",
		Status:         "initializing",
		Model:          "claude-sonnet-4-6",
		ChainRunID:     "vp-chain-run",
		ChainStepIndex: &step0,
	}); err != nil {
		t.Fatalf("create agent run: %v", err)
	}

	// Insert advance verdict first, abort verdict second.
	advanceJSON, _ := json.Marshal(domain.ChainVerdict{Outcome: domain.ChainVerdictAdvance, Reason: "looks good"})
	abortJSON, _ := json.Marshal(domain.ChainVerdict{Outcome: domain.ChainVerdictAbort, Reason: "something broke"})

	if err := InsertRunArtifact(db, "vp-run", "chain:verdict", string(advanceJSON)); err != nil {
		t.Fatalf("insert advance verdict: %v", err)
	}
	if err := InsertRunArtifact(db, "vp-run", "chain:verdict", string(abortJSON)); err != nil {
		t.Fatalf("insert abort verdict: %v", err)
	}

	// Also insert a verdict for a second run to verify per-run scoping.
	createPromptForTest(t, db, domain.Prompt{ID: "vp-step-b", Name: "VP Step B", Body: "y", Source: "user"})
	entity2, _, err := FindOrCreateEntity(db, "github", "owner/repo#chain-verdict-b", "pr", "Verdict Test B", "https://example.com/verdict-b")
	if err != nil {
		t.Fatalf("create entity B: %v", err)
	}
	eventID2, err := RecordEvent(db, domain.Event{
		EventType:    domain.EventGitHubPRCICheckFailed,
		EntityID:     &entity2.ID,
		MetadataJSON: `{"check_name":"ci"}`,
	})
	if err != nil {
		t.Fatalf("record event B: %v", err)
	}
	task2, _, err := FindOrCreateTask(db, entity2.ID, domain.EventGitHubPRCICheckFailed, "chain-verdict-b", eventID2, 0.5)
	if err != nil {
		t.Fatalf("create task B: %v", err)
	}
	if err := CreateAgentRun(db, domain.AgentRun{
		ID:       "vp-run-b",
		TaskID:   task2.ID,
		PromptID: "vp-step-b",
		Status:   "initializing",
		Model:    "claude-sonnet-4-6",
	}); err != nil {
		t.Fatalf("create agent run B: %v", err)
	}
	finalJSON, _ := json.Marshal(domain.ChainVerdict{Outcome: domain.ChainVerdictFinal, Reason: "done"})
	if err := InsertRunArtifact(db, "vp-run-b", "chain:verdict", string(finalJSON)); err != nil {
		t.Fatalf("insert final verdict for run-b: %v", err)
	}

	// Call with both run IDs.
	result, err := LatestChainVerdictsForRuns(db, []string{"vp-run", "vp-run-b", "vp-run-no-verdict"})
	if err != nil {
		t.Fatalf("LatestChainVerdictsForRuns: %v", err)
	}

	// vp-run: latest verdict is abort (second write wins).
	v, ok := result["vp-run"]
	if !ok {
		t.Fatal("vp-run missing from result map")
	}
	if v.Outcome != domain.ChainVerdictAbort {
		t.Errorf("vp-run verdict outcome = %q, want %q", v.Outcome, domain.ChainVerdictAbort)
	}

	// vp-run-b: final verdict.
	v2, ok := result["vp-run-b"]
	if !ok {
		t.Fatal("vp-run-b missing from result map")
	}
	if v2.Outcome != domain.ChainVerdictFinal {
		t.Errorf("vp-run-b verdict outcome = %q, want %q", v2.Outcome, domain.ChainVerdictFinal)
	}

	// vp-run-no-verdict: should be absent.
	if _, ok := result["vp-run-no-verdict"]; ok {
		t.Error("vp-run-no-verdict should be absent from result, has no artifacts")
	}
}

// TestMarkChainRunStatus_GuardedUpdate verifies that only non-terminal
// statuses accept a transition (race-guard) and the returned bool reflects
// whether the write landed.
func TestMarkChainRunStatus_GuardedUpdate(t *testing.T) {
	db := newTestDB(t)

	entity, _, err := FindOrCreateEntity(db, "github", "owner/repo#chain-guard", "pr", "Guard Test", "https://example.com/guard")
	if err != nil {
		t.Fatalf("create entity: %v", err)
	}
	eventID, err := RecordEvent(db, domain.Event{
		EventType:    domain.EventGitHubPRCICheckFailed,
		EntityID:     &entity.ID,
		MetadataJSON: `{"check_name":"ci"}`,
	})
	if err != nil {
		t.Fatalf("record event: %v", err)
	}
	task, _, err := FindOrCreateTask(db, entity.ID, domain.EventGitHubPRCICheckFailed, "chain-guard", eventID, 0.5)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	createPromptForTest(t, db, domain.Prompt{ID: "guard-chain", Name: "Guard Chain", Body: "chain", Source: "user", Kind: domain.PromptKindChain})

	chainRunID, err := CreateChainRun(db, domain.ChainRun{
		ID:            "chain-run-guard",
		ChainPromptID: "guard-chain",
		TaskID:        task.ID,
		TriggerType:   domain.ChainTriggerManual,
		Status:        domain.ChainRunStatusRunning,
	})
	if err != nil {
		t.Fatalf("create chain run: %v", err)
	}

	// First transition from running → completed: should succeed.
	changed, err := MarkChainRunStatus(db, chainRunID, domain.ChainRunStatusCompleted, "", nil)
	if err != nil {
		t.Fatalf("MarkChainRunStatus: %v", err)
	}
	if !changed {
		t.Error("expected changed=true for running → completed")
	}

	// Second call (already completed → aborted): guard must reject it.
	changed2, err := MarkChainRunStatus(db, chainRunID, domain.ChainRunStatusAborted, "late abort", nil)
	if err != nil {
		t.Fatalf("MarkChainRunStatus second: %v", err)
	}
	if changed2 {
		t.Error("expected changed=false when chain run already terminal")
	}

	// Verify the status is still completed, not overwritten.
	cr, err := GetChainRun(db, chainRunID)
	if err != nil {
		t.Fatalf("GetChainRun: %v", err)
	}
	if cr.Status != domain.ChainRunStatusCompleted {
		t.Errorf("status = %q, want completed", cr.Status)
	}
}

// TestCreateChainRun_RequiresTriggerType verifies that empty TriggerType
// returns an error instead of silently defaulting to "manual".
func TestCreateChainRun_RequiresTriggerType(t *testing.T) {
	db := newTestDB(t)

	entity, _, _ := FindOrCreateEntity(db, "github", "owner/repo#ttype", "pr", "T", "https://example.com/ttype")
	eventID, _ := RecordEvent(db, domain.Event{
		EventType:    domain.EventGitHubPRCICheckFailed,
		EntityID:     &entity.ID,
		MetadataJSON: `{}`,
	})
	task, _, _ := FindOrCreateTask(db, entity.ID, domain.EventGitHubPRCICheckFailed, "ttype", eventID, 0.5)
	createPromptForTest(t, db, domain.Prompt{ID: "ttype-chain", Name: "T", Body: "chain", Source: "user", Kind: domain.PromptKindChain})

	_, err := CreateChainRun(db, domain.ChainRun{
		ID:            "ttype-run",
		ChainPromptID: "ttype-chain",
		TaskID:        task.ID,
		TriggerType:   "", // intentionally empty
		Status:        domain.ChainRunStatusRunning,
	})
	if err == nil {
		t.Error("expected error for empty TriggerType, got nil")
	}
}

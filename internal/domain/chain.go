package domain

import "time"

// PromptKindLeaf and PromptKindChain are the two values of Prompt.Kind.
// Leaf is the existing single-prompt model; chain is an ordered list of
// leaf prompts executed sequentially against a shared worktree.
const (
	PromptKindLeaf  = "leaf"
	PromptKindChain = "chain"
)

// ChainRun statuses. running until any terminal; aborted when a step
// records --abort or omits a verdict; failed for infrastructure errors;
// cancelled when the user cancels mid-chain.
const (
	ChainRunStatusRunning   = "running"
	ChainRunStatusCompleted = "completed"
	ChainRunStatusAborted   = "aborted"
	ChainRunStatusFailed    = "failed"
	ChainRunStatusCancelled = "cancelled"
)

// ChainStep is one position in a chain prompt's ordered step list.
// step_index is 0-based and densely packed by ReplaceChainSteps.
type ChainStep struct {
	ChainPromptID string    `json:"chain_prompt_id"`
	StepIndex     int       `json:"step_index"`
	StepPromptID  string    `json:"step_prompt_id"`
	Brief         string    `json:"brief"`
	CreatedAt     time.Time `json:"created_at"`
}

// ChainRun is the chain instance. One row per Delegate(chainPrompt, ...)
// call. Owns the shared worktree across all steps. Per-step state
// lives on the runs table linked back via runs.chain_run_id.
type ChainRun struct {
	ID             string     `json:"id"`
	ChainPromptID  string     `json:"chain_prompt_id"`
	TaskID         string     `json:"task_id"`
	TriggerType    string     `json:"trigger_type"`
	TriggerID      string     `json:"trigger_id,omitempty"`
	Status         string     `json:"status"`
	AbortReason    string     `json:"abort_reason,omitempty"`
	AbortedAtStep  *int       `json:"aborted_at_step,omitempty"`
	WorktreePath   string     `json:"worktree_path"`
	StartedAt      time.Time  `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}

// ChainVerdict is the structured handoff a chain step records via
// `triagefactory exec chain verdict`. Stored as run_artifacts.metadata_json
// with kind='chain:verdict'. Latest by created_at wins per step (idempotent
// re-recording within a step).
//
// Tri-state semantics encoded in (Proceed, Final):
//   - Proceed=true,  Final=false → advance to next step
//   - Proceed=false, Final=false → abort the chain; leave task open for human
//   - Proceed=false, Final=true  → end the chain successfully at this step;
//     close the task. The step is allowed one terminal external action
//     (e.g., posting a SKIP review) which still flows through the existing
//     human-approval gate.
//
// Final=true with Proceed=true is invalid; the CLI rejects it.
type ChainVerdict struct {
	Proceed   bool   `json:"proceed"`
	Final     bool   `json:"final,omitempty"`
	Reason    string `json:"reason"`
	Notes     string `json:"notes,omitempty"`
	Synthetic bool   `json:"synthetic,omitempty"` // set when the orchestrator inserts a no-verdict default
}

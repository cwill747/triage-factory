package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/sky-ai-eng/triage-factory/internal/domain"
)

// ListChainSteps returns the ordered step list for a chain prompt.
// Empty slice (not error) when the prompt has no steps configured —
// the orchestrator treats that as a misconfigured chain and aborts.
func ListChainSteps(database *sql.DB, chainPromptID string) ([]domain.ChainStep, error) {
	rows, err := database.Query(`
		SELECT chain_prompt_id, step_index, step_prompt_id, brief, created_at
		FROM prompt_chain_steps
		WHERE chain_prompt_id = ?
		ORDER BY step_index ASC
	`, chainPromptID)
	if err != nil {
		return nil, fmt.Errorf("query chain steps: %w", err)
	}
	defer rows.Close()

	var out []domain.ChainStep
	for rows.Next() {
		var s domain.ChainStep
		if err := rows.Scan(&s.ChainPromptID, &s.StepIndex, &s.StepPromptID, &s.Brief, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CountChainStepReferences returns the number of chain prompts that
// reference the given prompt as a step. Used by the prompt-delete
// handler to surface "used by N chain(s)" instead of letting the FK
// RESTRICT raise a generic constraint error.
func CountChainStepReferences(database *sql.DB, stepPromptID string) (int, error) {
	var n int
	err := database.QueryRow(`
		SELECT COUNT(DISTINCT chain_prompt_id)
		FROM prompt_chain_steps
		WHERE step_prompt_id = ?
	`, stepPromptID).Scan(&n)
	return n, err
}

// ReplaceChainSteps replaces the entire step list for a chain prompt
// in a single transaction. The caller passes step prompt IDs in order;
// step_index is densely packed 0..N-1 by the writer, so callers don't
// need to manage indices. Briefs are taken positionally.
//
// The caller is responsible for upstream validation (rejecting nested
// chains, missing prompt IDs, etc.); this function will fail-fast on
// FK violations but does not produce friendly errors.
func ReplaceChainSteps(database *sql.DB, chainPromptID string, stepPromptIDs []string, briefs []string) error {
	if len(briefs) != 0 && len(briefs) != len(stepPromptIDs) {
		return fmt.Errorf("briefs length %d must match stepPromptIDs length %d", len(briefs), len(stepPromptIDs))
	}
	tx, err := database.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM prompt_chain_steps WHERE chain_prompt_id = ?`, chainPromptID); err != nil {
		return fmt.Errorf("clear existing steps: %w", err)
	}

	now := time.Now()
	for i, stepID := range stepPromptIDs {
		brief := ""
		if i < len(briefs) {
			brief = briefs[i]
		}
		if _, err := tx.Exec(`
			INSERT INTO prompt_chain_steps (chain_prompt_id, step_index, step_prompt_id, brief, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, chainPromptID, i, stepID, brief, now); err != nil {
			return fmt.Errorf("insert step %d: %w", i, err)
		}
	}
	return tx.Commit()
}

// CreateChainRun inserts a new chain instance row. The caller supplies
// the worktree path produced by setupGitHub/setupJira (the chain owns
// the worktree across all steps). Returns the generated id.
func CreateChainRun(database *sql.DB, cr domain.ChainRun) (string, error) {
	if cr.ID == "" {
		cr.ID = uuid.New().String()
	}
	if cr.Status == "" {
		cr.Status = domain.ChainRunStatusRunning
	}
	if cr.TriggerType == "" {
		cr.TriggerType = "manual"
	}
	var triggerID interface{}
	if cr.TriggerID != "" {
		triggerID = cr.TriggerID
	}
	_, err := database.Exec(`
		INSERT INTO chain_runs (id, chain_prompt_id, task_id, trigger_type, trigger_id, status, worktree_path, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`, cr.ID, cr.ChainPromptID, cr.TaskID, cr.TriggerType, triggerID, cr.Status, cr.WorktreePath)
	if err != nil {
		return "", fmt.Errorf("insert chain_run: %w", err)
	}
	return cr.ID, nil
}

// GetChainRun returns a chain run by ID. Returns (nil, nil) if not found.
func GetChainRun(database *sql.DB, id string) (*domain.ChainRun, error) {
	var (
		cr            domain.ChainRun
		triggerID     sql.NullString
		abortReason   sql.NullString
		abortedAtStep sql.NullInt64
		completedAt   sql.NullTime
	)
	err := database.QueryRow(`
		SELECT id, chain_prompt_id, task_id, trigger_type, trigger_id, status,
		       abort_reason, aborted_at_step, worktree_path, started_at, completed_at
		FROM chain_runs WHERE id = ?
	`, id).Scan(&cr.ID, &cr.ChainPromptID, &cr.TaskID, &cr.TriggerType, &triggerID, &cr.Status,
		&abortReason, &abortedAtStep, &cr.WorktreePath, &cr.StartedAt, &completedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if triggerID.Valid {
		cr.TriggerID = triggerID.String
	}
	if abortReason.Valid {
		cr.AbortReason = abortReason.String
	}
	if abortedAtStep.Valid {
		i := int(abortedAtStep.Int64)
		cr.AbortedAtStep = &i
	}
	if completedAt.Valid {
		t := completedAt.Time
		cr.CompletedAt = &t
	}
	return &cr, nil
}

// GetChainRunForRun returns the chain run that owns a step run, if any.
// Returns (nil, nil) when the run is a single (non-chain) run.
func GetChainRunForRun(database *sql.DB, runID string) (*domain.ChainRun, *int, error) {
	var (
		chainRunID sql.NullString
		stepIndex  sql.NullInt64
	)
	err := database.QueryRow(`SELECT chain_run_id, chain_step_index FROM runs WHERE id = ?`, runID).
		Scan(&chainRunID, &stepIndex)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if !chainRunID.Valid {
		return nil, nil, nil
	}
	cr, err := GetChainRun(database, chainRunID.String)
	if err != nil || cr == nil {
		return nil, nil, err
	}
	var idx *int
	if stepIndex.Valid {
		v := int(stepIndex.Int64)
		idx = &v
	}
	return cr, idx, nil
}

// MarkChainRunStatus transitions a chain run to a terminal status and
// records optional abort metadata. completed_at is set on every
// transition out of 'running' so the lifetime is queryable.
func MarkChainRunStatus(database *sql.DB, id, status, abortReason string, abortedAtStep *int) error {
	var (
		reasonArg interface{}
		stepArg   interface{}
	)
	if abortReason != "" {
		reasonArg = abortReason
	}
	if abortedAtStep != nil {
		stepArg = *abortedAtStep
	}
	completedAt := time.Now()
	_, err := database.Exec(`
		UPDATE chain_runs
		SET status = ?, abort_reason = ?, aborted_at_step = ?, completed_at = ?
		WHERE id = ?
	`, status, reasonArg, stepArg, completedAt, id)
	return err
}

// GetLatestChainVerdict reads the most recent chain:verdict artifact
// recorded by the step's run. Returns (nil, nil) when no verdict
// exists — the orchestrator treats that as the "no-verdict" abort
// default.
func GetLatestChainVerdict(database *sql.DB, runID string) (*domain.ChainVerdict, error) {
	var raw string
	err := database.QueryRow(`
		SELECT metadata_json FROM run_artifacts
		WHERE run_id = ? AND kind = 'chain:verdict'
		ORDER BY created_at DESC LIMIT 1
	`, runID).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return &domain.ChainVerdict{}, nil
	}
	var v domain.ChainVerdict
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, fmt.Errorf("decode verdict: %w", err)
	}
	return &v, nil
}

// RunsForChain returns every step run linked to a chain instance,
// ordered by chain_step_index ASC. Used by the chain-detail HTTP
// endpoint to render the per-step timeline in a single fetch.
func RunsForChain(database *sql.DB, chainRunID string) ([]domain.AgentRun, error) {
	rows, err := database.Query(`
		SELECT id, task_id, prompt_id, status, model, started_at, completed_at,
		       total_cost_usd, duration_ms, num_turns, stop_reason, worktree_path,
		       result_summary, session_id, chain_run_id, chain_step_index
		FROM runs
		WHERE chain_run_id = ?
		ORDER BY chain_step_index ASC, started_at ASC
	`, chainRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.AgentRun
	for rows.Next() {
		var (
			r           domain.AgentRun
			completedAt sql.NullTime
			costUSD     sql.NullFloat64
			durationMs  sql.NullInt64
			numTurns    sql.NullInt64
			chainStep   sql.NullInt64
			promptID    sql.NullString
			model       sql.NullString
			stopReason  sql.NullString
			worktreeP   sql.NullString
			resultSum   sql.NullString
			sessionID   sql.NullString
			chainRunIDs sql.NullString
		)
		if err := rows.Scan(&r.ID, &r.TaskID, &promptID, &r.Status, &model, &r.StartedAt, &completedAt,
			&costUSD, &durationMs, &numTurns, &stopReason, &worktreeP, &resultSum, &sessionID,
			&chainRunIDs, &chainStep); err != nil {
			return nil, err
		}
		r.PromptID = promptID.String
		r.Model = model.String
		r.StopReason = stopReason.String
		r.WorktreePath = worktreeP.String
		r.ResultSummary = resultSum.String
		r.SessionID = sessionID.String
		if chainRunIDs.Valid {
			r.ChainRunID = chainRunIDs.String
		}
		if chainStep.Valid {
			v := int(chainStep.Int64)
			r.ChainStepIndex = &v
		}
		if completedAt.Valid {
			r.CompletedAt = &completedAt.Time
		}
		if costUSD.Valid {
			r.TotalCostUSD = &costUSD.Float64
		}
		if durationMs.Valid {
			v := int(durationMs.Int64)
			r.DurationMs = &v
		}
		if numTurns.Valid {
			v := int(numTurns.Int64)
			r.NumTurns = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertRunArtifact creates a non-primary artifact row. Currently used
// by the chain-verdict pipeline (both the exec subcommand and the
// synthetic no-verdict default written by the orchestrator).
func InsertRunArtifact(database *sql.DB, runID, kind, metadataJSON string) error {
	id := uuid.New().String()
	_, err := database.Exec(`
		INSERT INTO run_artifacts (id, run_id, kind, metadata_json, is_primary, created_at)
		VALUES (?, ?, ?, ?, 0, CURRENT_TIMESTAMP)
	`, id, runID, kind, metadataJSON)
	return err
}

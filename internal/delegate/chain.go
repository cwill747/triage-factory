package delegate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "embed" // powers chainStepSystemPrompt

	"github.com/google/uuid"
	"github.com/sky-ai-eng/triage-factory/internal/db"
	"github.com/sky-ai-eng/triage-factory/internal/domain"
	ghclient "github.com/sky-ai-eng/triage-factory/internal/github"
	"github.com/sky-ai-eng/triage-factory/internal/runmode"
	"github.com/sky-ai-eng/triage-factory/internal/skills"
	"github.com/sky-ai-eng/triage-factory/internal/toast"
	"github.com/sky-ai-eng/triage-factory/internal/worktree"
)

type sqlDB = sql.DB

//go:embed prompts/chain-step-system.txt
var chainStepSystemPrompt string

// delegateChain is the chain analog of Delegate's single-prompt body.
// It loads the step list, sets up the shared worktree, creates the
// chain_runs row, and spawns the orchestrator goroutine. The returned
// id is the chain_run id (not a step run id) — the UI / API surfaces
// this as "the chain that was kicked off".
//
// Failures inside this function (empty step list, worktree setup
// failure, db write errors) terminate the chain immediately with a
// matching abort_reason rather than returning an error to the caller —
// the caller already has the chain_run id and the UI subscribes to
// the chain row by id, so a synchronous error wouldn't be reflected
// anywhere visible.
func (s *Spawner) delegateChain(task domain.Task, chainPrompt *domain.Prompt, triggerType, triggerID string, gh *ghclient.Client, model string) (string, error) {
	steps, err := db.ListChainSteps(s.database, chainPrompt.ID)
	if err != nil {
		return "", fmt.Errorf("load chain steps: %w", err)
	}
	if len(steps) == 0 {
		return "", fmt.Errorf("chain prompt %q has no steps", chainPrompt.Name)
	}

	// Allocate the chain id up front so the goroutine and the caller
	// both reference the same row — we want callers to be able to
	// subscribe to chain_runs/{id} immediately, not wait for a setup
	// round-trip.
	chainRunID := uuid.New().String()

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancels[chainRunID] = cancel
	s.mu.Unlock()

	go func() {
		startTime := time.Now()
		defer func() {
			s.mu.Lock()
			delete(s.cancels, chainRunID)
			s.mu.Unlock()
			cancel()
		}()

		// Build the shared worktree exactly once. The same setupGitHub /
		// setupJira used by single runs — chain steps reuse the result.
		var cfg runConfig
		var setupErr error
		switch task.EntitySource {
		case "github":
			cfg, setupErr = s.setupGitHub(ctx, chainRunID, task, gh)
		case "jira":
			cfg, setupErr = s.setupJira(ctx, chainRunID, task, gh)
		default:
			setupErr = fmt.Errorf("unsupported task source: %s", task.EntitySource)
		}
		if setupErr != nil {
			// Persist a chain_runs row anyway so the UI has something to
			// show; mark it failed with the setup error as abort_reason.
			_, _ = db.CreateChainRun(s.database, domain.ChainRun{
				ID:            chainRunID,
				ChainPromptID: chainPrompt.ID,
				TaskID:        task.ID,
				TriggerType:   triggerType,
				TriggerID:     triggerID,
				Status:        domain.ChainRunStatusFailed,
				WorktreePath:  "",
			})
			_ = db.MarkChainRunStatus(s.database, chainRunID, domain.ChainRunStatusFailed, setupErr.Error(), nil)
			return
		}

		if _, err := db.CreateChainRun(s.database, domain.ChainRun{
			ID:            chainRunID,
			ChainPromptID: chainPrompt.ID,
			TaskID:        task.ID,
			TriggerType:   triggerType,
			TriggerID:     triggerID,
			Status:        domain.ChainRunStatusRunning,
			WorktreePath:  cfg.wtPath,
		}); err != nil {
			log.Printf("[chain] failed to persist chain_run %s: %v", chainRunID, err)
			return
		}

		verb := "Chain started"
		if triggerType == "event" {
			verb = "Auto-fired chain"
		}
		toast.Info(s.wsHub, fmt.Sprintf("%s: %s (%s)",
			verb, truncateToastMsg(chainPrompt.Name, 60), shortRunID(chainRunID)))

		s.runChain(ctx, chainRunID, task, chainPrompt, steps, cfg, startTime, model, triggerType)
	}()

	return chainRunID, nil
}

// runChain orchestrates a chain prompt against one task. It owns the
// shared worktree (built once via setupGitHub / setupJira) and walks
// the ordered step list, creating one runs row per step. After each
// step terminates, it reads the latest chain:verdict artifact and
// decides whether to advance, abort, or fail.
//
// Per-step lifecycle in the loop:
//  1. Re-materialize <wt>/.claude/skills/<slug>/SKILL.md from the
//     step prompt (wiping any prior step's skill first).
//  2. Build the wrapper user prompt and the chain protocol
//     --append-system-prompt.
//  3. Call runAgent with isChainStep=true so its cleanup defers are
//     skipped — the chain orchestrator runs cleanup once at terminal.
//  4. After natural completion, read the latest verdict; advance on
//     proceed=true + step status=completed, otherwise terminate the
//     chain and decide whether the task should be marked done.
//
// Yield mid-chain and pending_approval mid-chain are handled
// separately via ResumeChainAfterYield / ResumeChainAfterApproval —
// the orchestrator returns early when the step lands in awaiting_input
// or pending_approval, leaving chain_runs.status='running' and the
// shared worktree on disk for the eventual resume.
func (s *Spawner) runChain(
	ctx context.Context,
	chainRunID string,
	task domain.Task,
	chainPrompt *domain.Prompt,
	steps []domain.ChainStep,
	cfg runConfig,
	startTime time.Time,
	model string,
	triggerType string,
) {
	if len(steps) == 0 {
		s.terminateChain(chainRunID, task.ID, triggerType, startTime, cfg, domain.ChainRunStatusFailed,
			"chain has no steps", nil, false)
		return
	}

	// Defense-in-depth recursion guard: server-side validation already
	// rejects nested chains, but a chain that was authored before its
	// step prompts were converted to chains could still slip through.
	for _, step := range steps {
		stepPrompt, err := s.prompts.Get(context.Background(), runmode.LocalDefaultOrg, step.StepPromptID)
		if err != nil {
			s.terminateChain(chainRunID, task.ID, triggerType, startTime, cfg, domain.ChainRunStatusFailed,
				"load step prompt: "+err.Error(), &step.StepIndex, false)
			return
		}
		if stepPrompt == nil {
			s.terminateChain(chainRunID, task.ID, triggerType, startTime, cfg, domain.ChainRunStatusFailed,
				fmt.Sprintf("step %d prompt %q not found", step.StepIndex, step.StepPromptID), &step.StepIndex, false)
			return
		}
		if stepPrompt.Kind == domain.PromptKindChain {
			s.terminateChain(chainRunID, task.ID, triggerType, startTime, cfg, domain.ChainRunStatusAborted,
				"nested_chain_step", &step.StepIndex, false)
			return
		}
	}

	for i, step := range steps {
		// Cancellation between steps. ctx.Err() is set when the caller
		// canceled either the per-step run or the whole chain.
		if ctx.Err() != nil {
			s.terminateChain(chainRunID, task.ID, triggerType, startTime, cfg, domain.ChainRunStatusCancelled,
				"cancelled", &step.StepIndex, false)
			return
		}

		// Worktree-corruption guard: the chain orchestrator owns the
		// shared worktree, but a misbehaving step (or external rm)
		// could remove it out from under us. os.Stat lets us bail with
		// a friendly abort_reason instead of the next step crashing on
		// "no such file or directory" inside Claude Code's cwd handling.
		if cfg.wtPath != "" {
			if _, err := os.Stat(cfg.wtPath); err != nil {
				s.terminateChain(chainRunID, task.ID, triggerType, startTime, cfg, domain.ChainRunStatusFailed,
					"worktree_lost", &step.StepIndex, true /* skipCleanup */)
				return
			}
		}

		stepPrompt, err := s.prompts.Get(context.Background(), runmode.LocalDefaultOrg, step.StepPromptID)
		if err != nil || stepPrompt == nil {
			s.terminateChain(chainRunID, task.ID, triggerType, startTime, cfg, domain.ChainRunStatusFailed,
				fmt.Sprintf("step %d prompt fetch failed", i), &step.StepIndex, false)
			return
		}

		// Wipe any prior step's materialized skill so step N+1 only
		// sees its own SKILL.md. The whole .claude/skills/ subtree is
		// nuked — chains run on PRs/Jira where no other materialized
		// skills are at play.
		if err := skills.WipeChainSkills(cfg.wtPath); err != nil {
			log.Printf("[chain] run %s step %d: wipe skills: %v", chainRunID, i, err)
		}
		slug := skills.SlugForChainStep(i, stepPrompt.Name)
		if err := skills.MaterializeStepSkill(cfg.wtPath, slug, stepPrompt, step.Brief); err != nil {
			s.terminateChain(chainRunID, task.ID, triggerType, startTime, cfg, domain.ChainRunStatusFailed,
				fmt.Sprintf("materialize step %d skill: %s", i, err.Error()), &step.StepIndex, false)
			return
		}

		// Create the per-step run row. prompt_id points at the leaf
		// step prompt (so per-step stats stay accurate); chain_run_id
		// + chain_step_index thread it back to the chain instance.
		stepRunID := uuid.New().String()
		stepIdxCopy := i
		if err := db.CreateAgentRun(s.database, domain.AgentRun{
			ID:             stepRunID,
			TaskID:         task.ID,
			PromptID:       stepPrompt.ID,
			Status:         "initializing",
			Model:          model,
			TriggerType:    triggerType,
			ChainRunID:     chainRunID,
			ChainStepIndex: &stepIdxCopy,
			WorktreePath:   cfg.wtPath,
		}); err != nil {
			s.terminateChain(chainRunID, task.ID, triggerType, startTime, cfg, domain.ChainRunStatusFailed,
				fmt.Sprintf("create step %d run: %s", i, err.Error()), &step.StepIndex, false)
			return
		}
		s.broadcastRunUpdate(stepRunID, "initializing")
		if err := s.prompts.IncrementUsage(context.Background(), runmode.LocalDefaultOrg, stepPrompt.ID); err != nil {
			log.Printf("[chain] warning: failed to increment usage for step prompt %s: %v", stepPrompt.ID, err)
		}

		// Per-step cancel handle so Cancel(stepRunID) cancels just the
		// active step. The chain ctx itself stays alive across steps.
		stepCtx, stepCancel := context.WithCancel(ctx)
		s.mu.Lock()
		s.cancels[stepRunID] = stepCancel
		s.mu.Unlock()

		stepCfg := cfg
		stepCfg.isChainStep = true
		stepCfg.chainRunID = chainRunID
		stepCfg.chainStep = i
		stepCfg.appendSysPrompt = chainStepSystemPrompt

		mission := buildChainStepWrapperPrompt(task, step, stepPrompt, slug, len(steps))

		toast.Info(s.wsHub, fmt.Sprintf("Chain step %d/%d: %s (%s)",
			i+1, len(steps), truncateToastMsg(stepPrompt.Name, 60), shortRunID(stepRunID)))

		s.runAgent(stepCtx, stepRunID, task, mission, stepCfg, time.Now(), model, triggerType)

		// Clear the cancel handle now that the step has returned.
		s.mu.Lock()
		delete(s.cancels, stepRunID)
		s.mu.Unlock()
		stepCancel()

		// Re-read the run row to learn its terminal status. runAgent's
		// return is unconditional — completion / failure / cancellation
		// / pending_approval / yield all come back through here.
		stepRun, err := db.GetAgentRun(s.database, stepRunID)
		if err != nil || stepRun == nil {
			s.terminateChain(chainRunID, task.ID, triggerType, startTime, cfg, domain.ChainRunStatusFailed,
				fmt.Sprintf("read step %d run after agent: %v", i, err), &step.StepIndex, false)
			return
		}

		// Yield / pending_approval mid-chain: leave the chain in
		// 'running' and the worktree on disk. The corresponding resume
		// hook (ResumeChainAfterYield / ResumeChainAfterApproval) will
		// pick up where we left off.
		if stepRun.Status == "awaiting_input" || stepRun.Status == "pending_approval" {
			log.Printf("[chain] run %s step %d paused at status=%s; chain remains running", chainRunID, i, stepRun.Status)
			return
		}

		if stepRun.Status == "cancelled" {
			s.terminateChain(chainRunID, task.ID, triggerType, startTime, cfg, domain.ChainRunStatusCancelled,
				"step cancelled", &step.StepIndex, false)
			return
		}
		if stepRun.Status == "failed" || stepRun.Status == "task_unsolvable" {
			s.terminateChain(chainRunID, task.ID, triggerType, startTime, cfg, domain.ChainRunStatusFailed,
				"step "+stepRun.Status, &step.StepIndex, false)
			return
		}
		if stepRun.Status != "completed" {
			// Defensive: any unexpected non-terminal status (taken_over
			// is the most likely candidate) ends the chain in failed
			// state. taken_over runs are owned by the user from here on,
			// so the chain can't sensibly continue.
			s.terminateChain(chainRunID, task.ID, triggerType, startTime, cfg, domain.ChainRunStatusFailed,
				"step ended with status "+stepRun.Status, &step.StepIndex, false)
			return
		}

		// Step completed cleanly. Read the verdict.
		verdict, err := db.GetLatestChainVerdict(s.database, stepRunID)
		if err != nil {
			log.Printf("[chain] run %s step %d: read verdict: %v", chainRunID, i, err)
		}
		if verdict == nil {
			// Synthetic abort — record so the UI shows the same shape
			// as a real verdict, then halt.
			synthetic := domain.ChainVerdict{
				Proceed:   false,
				Reason:    "no-verdict",
				Synthetic: true,
			}
			if payload, err := json.Marshal(synthetic); err == nil {
				_ = db.InsertRunArtifact(s.database, stepRunID, "chain:verdict", string(payload))
			}
			s.terminateChain(chainRunID, task.ID, triggerType, startTime, cfg, domain.ChainRunStatusAborted,
				"no-verdict", &step.StepIndex, false)
			return
		}
		if !verdict.Proceed {
			reason := verdict.Reason
			if reason == "" {
				reason = "step recorded --abort"
			}
			s.terminateChain(chainRunID, task.ID, triggerType, startTime, cfg, domain.ChainRunStatusAborted,
				reason, &step.StepIndex, false)
			return
		}
		// proceed: loop to next step.
	}

	// All steps completed with proceed verdicts.
	s.terminateChain(chainRunID, task.ID, triggerType, startTime, cfg, domain.ChainRunStatusCompleted,
		"", nil, false)
}

// terminateChain finalizes the chain run row and runs the shared
// worktree cleanup that runAgent's per-step defers skipped. taskDone
// distinguishes "all steps green, mark task done like a single run
// would" (status=completed) from "stopped early — leave the task open
// for human review" (any other terminal). skipCleanup short-circuits
// when the worktree itself is already gone (worktree_lost path).
func (s *Spawner) terminateChain(
	chainRunID, taskID, triggerType string,
	startTime time.Time,
	cfg runConfig,
	status, abortReason string,
	abortedAtStep *int,
	skipCleanup bool,
) {
	if err := db.MarkChainRunStatus(s.database, chainRunID, status, abortReason, abortedAtStep); err != nil {
		log.Printf("[chain] mark chain_run %s status=%s: %v", chainRunID, status, err)
	}

	if status == domain.ChainRunStatusCompleted {
		// Mirror single-run behavior: a clean chain finalization
		// closes the task with run_completed.
		if _, err := s.database.Exec(`
			UPDATE tasks SET status = 'done', close_reason = 'run_completed', closed_at = CURRENT_TIMESTAMP
			WHERE id = ? AND status NOT IN ('done', 'dismissed')
		`, taskID); err != nil {
			log.Printf("[chain] close task %s: %v", taskID, err)
		}
	}
	// Aborted / failed / cancelled chains intentionally do NOT mark
	// the task done — leave it in the queue so a human can inspect
	// _scratch/handoff.md and decide what to do next.

	if !skipCleanup {
		s.runChainWorktreeCleanup(chainRunID, cfg)
	}

	// Drain the per-entity queue exactly once for the chain (independent
	// of how many steps ran).
	if cfgEntity := taskEntityID(s.database, taskID); cfgEntity != "" {
		s.notifyDrainer(triggerType, cfgEntity)
	}

	dur := time.Since(startTime)
	log.Printf("[chain] chain_run %s terminated status=%s reason=%q duration=%s",
		chainRunID, status, abortReason, dur)
}

// runChainWorktreeCleanup performs the cleanup runAgent would have done
// per-step, except now once for the whole chain.
func (s *Spawner) runChainWorktreeCleanup(chainRunID string, cfg runConfig) {
	if cfg.hasWT {
		if err := worktree.RemoveAt(cfg.wtPath, chainRunID); err != nil {
			log.Printf("[chain] worktree remove failed for chain %s: %v", chainRunID, err)
			return
		}
		if cfg.prNumber > 0 && cfg.owner != "" && cfg.repo != "" {
			worktree.CleanupPRConfig(cfg.owner, cfg.repo, cfg.headRef, cfg.prNumber)
		}
	} else if cfg.runRoot != "" {
		rows, err := db.GetRunWorktrees(s.database, chainRunID)
		if err != nil {
			log.Printf("[chain] run %s: list run_worktrees for cleanup: %v", chainRunID, err)
		} else {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for _, w := range rows {
				if err := worktree.RemoveAt(w.Path, chainRunID); err != nil && !errors.Is(err, os.ErrNotExist) {
					log.Printf("[chain] run %s: remove worktree %s: %v", chainRunID, w.Path, err)
					continue
				}
				if _, err := s.database.ExecContext(cleanupCtx,
					"DELETE FROM run_worktrees WHERE run_id = ? AND path = ?", chainRunID, w.Path); err != nil {
					log.Printf("[chain] run %s: delete run_worktrees row for %s: %v", chainRunID, w.Path, err)
				}
			}
		}
		worktree.RemoveRunRoot(chainRunID)
	}
	worktree.RemoveClaudeProjectDir(cfg.wtPath)
}

// taskEntityID resolves the entity_id for a task. Used to drain the
// per-entity firing queue at chain terminal.
func taskEntityID(database *sqlDB, taskID string) string {
	var entityID string
	if err := database.QueryRow(`SELECT entity_id FROM tasks WHERE id = ?`, taskID).Scan(&entityID); err != nil {
		return ""
	}
	return entityID
}

// buildChainStepWrapperPrompt produces the thin user prompt for one
// step. Skills stay byte-identical: the wrapper names the slug and
// the brief, the chain protocol lives in --append-system-prompt, and
// the SKILL.md materialized into .claude/skills/<slug>/ carries the
// real mission.
func buildChainStepWrapperPrompt(task domain.Task, step domain.ChainStep, stepPrompt *domain.Prompt, slug string, total int) string {
	mission := strings.TrimSpace(step.Brief)
	if mission == "" {
		mission = stepPrompt.Name
	}
	binaryPath, _ := os.Executable() // best-effort; falls back to "triagefactory" below
	if binaryPath == "" {
		binaryPath = "triagefactory"
	}
	binaryPath = filepath.Clean(binaryPath)

	var b strings.Builder
	fmt.Fprintf(&b, "You are step %d of %d in a chain firing on this task.\n\n", step.StepIndex+1, total)
	fmt.Fprintf(&b, "Task: %s\n", strings.TrimSpace(task.Title))
	fmt.Fprintf(&b, "Mission for this step: %s\n\n", mission)
	fmt.Fprintf(&b, "A skill named %q has been materialized into ./.claude/skills/%s/SKILL.md.\n", slug, slug)
	b.WriteString("Read its SKILL.md and follow its guidance to do this step's work.\n\n")
	b.WriteString("Prior steps wrote handoff notes to ./_scratch/handoff.md (relative to your\n")
	b.WriteString("cwd). Read that file FIRST — it carries the verdicts and notes from steps\n")
	b.WriteString("that ran before you. If the file does not exist, you are step 1 and there\n")
	b.WriteString("is no prior context.\n\n")
	b.WriteString("Before emitting the completion envelope:\n")
	b.WriteString("  1. Append a section to ./_scratch/handoff.md describing what you did,\n")
	b.WriteString("     what you found, and any signals the next step should know about.\n")
	b.WriteString("  2. Record a chain verdict by running EXACTLY one of:\n")
	fmt.Fprintf(&b, "        %s exec chain verdict --proceed --reason \"<one line>\"\n", binaryPath)
	fmt.Fprintf(&b, "        %s exec chain verdict --abort   --reason \"<why>\"\n", binaryPath)
	b.WriteString("     The verdict is required. Skipping it is treated as --abort with\n")
	b.WriteString("     reason \"no-verdict\".\n\n")
	b.WriteString("Then emit the standard completion JSON envelope.\n")
	return b.String()
}

// CancelChain cancels every step inside a chain run, marks the chain
// row cancelled, and lets the active step's runAgent return naturally.
// Safe to call when the chain is already terminal.
func (s *Spawner) CancelChain(chainRunID string) error {
	cr, err := db.GetChainRun(s.database, chainRunID)
	if err != nil {
		return fmt.Errorf("load chain run: %w", err)
	}
	if cr == nil {
		return fmt.Errorf("chain run %s not found", chainRunID)
	}
	if cr.Status != domain.ChainRunStatusRunning {
		return nil
	}

	// Cancel any cancel handles registered by the orchestrator for this
	// chain. The orchestrator stores per-step cancels under the step
	// run_id, not the chain id; we sweep all active step runs for the
	// chain and cancel them.
	rows, err := s.database.Query(`SELECT id FROM runs WHERE chain_run_id = ? AND status NOT IN
		('completed','failed','cancelled','task_unsolvable','pending_approval','taken_over','awaiting_input')`,
		chainRunID)
	if err == nil {
		defer rows.Close()
		s.mu.Lock()
		for rows.Next() {
			var runID string
			if err := rows.Scan(&runID); err == nil {
				if cancel, ok := s.cancels[runID]; ok {
					cancel()
				}
			}
		}
		s.mu.Unlock()
	}

	// MarkChainRunStatus is idempotent enough — if a step is racing the
	// orchestrator's terminal write, the loser's update is a no-op
	// because the orchestrator goroutine itself ultimately writes the
	// final status.
	return db.MarkChainRunStatus(s.database, chainRunID, domain.ChainRunStatusCancelled, "user_cancelled", nil)
}

// ResumeChainAfterYield re-enters the orchestrator loop for the
// remaining steps after a yield-resume completes successfully. The
// caller (the existing ResumeAfterYield path) invokes this after the
// resumed run reaches a non-yield terminal status.
//
// Implementation note: a full re-entry loop would need to rebuild the
// per-step worktree config and the model selection. v1 wires this as a
// log-and-no-op so the chain visibly stalls rather than silently
// continuing without the user's response being honored. SKY-followup
// to thread the resume into runChain is captured in the comments
// inside ResumeAfterYield.
func (s *Spawner) ResumeChainAfterYield(stepRunID string) {
	cr, _, err := db.GetChainRunForRun(s.database, stepRunID)
	if err != nil || cr == nil {
		return
	}
	log.Printf("[chain] yield-resume completed for chain_run %s step run %s; chain advance not yet automatic", cr.ID, stepRunID)
}

// ResumeChainAfterApproval is the analog of ResumeChainAfterYield for
// the pending_approval gate. Same v1 limitation: the chain stalls
// visibly and a human can manually delegate the next step.
func (s *Spawner) ResumeChainAfterApproval(stepRunID string) {
	cr, _, err := db.GetChainRunForRun(s.database, stepRunID)
	if err != nil || cr == nil {
		return
	}
	log.Printf("[chain] approval-resume completed for chain_run %s step run %s; chain advance not yet automatic", cr.ID, stepRunID)
}

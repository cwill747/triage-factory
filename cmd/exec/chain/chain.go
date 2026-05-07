// Package chain implements the `triagefactory exec chain` CLI surface —
// agent-callable commands that the chain orchestrator reads to decide
// whether a chain proceeds to the next step or aborts.
//
// The flow: a chain step's wrapper user prompt instructs the agent to
// call `chain verdict --proceed|--abort --reason ...` before emitting
// its completion envelope. The verdict lands in run_artifacts with
// kind='chain:verdict' and a JSON metadata blob. The orchestrator
// reads the latest verdict for the step's run after Claude terminates;
// no verdict means abort with reason "no-verdict".
package chain

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/sky-ai-eng/triage-factory/internal/db"
	"github.com/sky-ai-eng/triage-factory/internal/domain"
)

// HelpText is the help block for `chain` commands.
const HelpText = `Chain Commands:
  chain verdict --proceed --reason <text> [--notes <text>]
  chain verdict --abort   --reason <text> [--notes <text>]

Records the chain step's verdict. Read by the orchestrator after the
step's completion envelope. Exactly one of --proceed / --abort must be
set. The verdict is persisted to run_artifacts(kind='chain:verdict');
the orchestrator picks the most recent verdict written by the step.

Idempotency: re-running this command in the same step appends a new
verdict artifact; the orchestrator reads the most recent. Use this if
you want to revise a verdict before emitting the completion envelope.

Run id is read from $TRIAGE_FACTORY_RUN_ID (set by the delegation
spawner). The command refuses to run when invoked outside a chain
step (the run has no chain_run_id).`

// Handle dispatches chain subcommands.
func Handle(database *db.DB, args []string) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printHelp()
		return
	}
	switch args[0] {
	case "verdict":
		runVerdict(database, args[1:])
	default:
		exitErr("unknown chain command: " + args[0])
	}
}

func printHelp() {
	fmt.Printf("Usage: triagefactory exec chain <command> [args]\n\n%s\n", HelpText)
}

func runVerdict(database *db.DB, args []string) {
	fs := flag.NewFlagSet("chain verdict", flag.ContinueOnError)
	var (
		proceed bool
		abort   bool
		reason  string
		notes   string
	)
	fs.BoolVar(&proceed, "proceed", false, "advance the chain to the next step")
	fs.BoolVar(&abort, "abort", false, "stop the chain")
	fs.StringVar(&reason, "reason", "", "one-line reason (required)")
	fs.StringVar(&notes, "notes", "", "optional longer notes")
	if err := fs.Parse(args); err != nil {
		exitErr("parse flags: " + err.Error())
	}
	if proceed == abort {
		// Both unset or both set — neither is a valid verdict.
		exitErr("exactly one of --proceed / --abort is required")
	}
	if reason == "" {
		exitErr("--reason is required")
	}

	runID := os.Getenv("TRIAGE_FACTORY_RUN_ID")
	if runID == "" {
		exitErr("TRIAGE_FACTORY_RUN_ID not set; chain verdict can only be recorded inside a delegation run")
	}

	chainRun, stepIdx, err := db.GetChainRunForRun(database.Conn, runID)
	if err != nil {
		exitErr("lookup chain run: " + err.Error())
	}
	if chainRun == nil {
		exitErr("this run is not part of a chain (no chain_run_id on the run row)")
	}
	if chainRun.Status != domain.ChainRunStatusRunning {
		exitErr(fmt.Sprintf("chain run %s is %s; cannot record a verdict", chainRun.ID, chainRun.Status))
	}

	verdict := domain.ChainVerdict{
		Proceed: proceed,
		Reason:  reason,
		Notes:   notes,
	}
	payload, err := json.Marshal(verdict)
	if err != nil {
		exitErr("encode verdict: " + err.Error())
	}
	if err := db.InsertRunArtifact(database.Conn, runID, "chain:verdict", string(payload)); err != nil {
		exitErr("record verdict: " + err.Error())
	}

	out := map[string]interface{}{
		"recorded": true,
		"step":     stepIdx,
		"proceed":  proceed,
	}
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(out); err != nil {
		exitErr("encode response: " + err.Error())
	}
}

func exitErr(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}

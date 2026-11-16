package vikunja

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/sky-ai-eng/triage-factory/cmd/exec"
	"github.com/sky-ai-eng/triage-factory/cmd/exec/agenthost"
	"github.com/sky-ai-eng/triage-factory/cmd/exec/execflags"
	"github.com/sky-ai-eng/triage-factory/cmd/exec/prog"
	"github.com/sky-ai-eng/triage-factory/internal/agentprompt"
)

const cliHelp = `Vikunja Commands:
  vikunja list --project <id>                               List a routed project's tasks
  vikunja view <task-id>                                    Read a task
  vikunja comments <task-id>                                Read task comments
  vikunja create --project <id> --title <text> [--description <text>] [--priority <0-5>]
  vikunja edit <task-id> [--title <text>] [--description <text>] [--priority <0-5>]
  vikunja complete <task-id>                                Mark a task done
  vikunja reopen <task-id>                                  Mark a task not done
  vikunja comment <task-id> --body <text>                    Post a comment

IDs are numeric IDs from Vikunja task/project URLs, not per-project task indexes.
Commands return JSON. Writes are audited. Only projects routed to the run's team are accessible.`

var cliValueFlags = map[string]bool{"--project": true, "--title": true, "--description": true, "--priority": true, "--body": true}

func init() {
	exec.RegisterSubcommand("vikunja", exec.Subcommand{Run: runCLI, HelpText: cliHelp, ValueFlags: cliValueFlags, SourceKind: "vikunja"})
	agentprompt.RegisterToolsReference("vikunja", `Use triagefactory exec vikunja to read and write Vikunja tasks. Run triagefactory exec vikunja --help for syntax.
The task ID is the numeric ID in the task URL or event metadata, not its project index.
Use view and comments to read context; list --project <id> finds related tasks in a routed project.
Use create, edit, complete, reopen, and comment to update Vikunja through the credential-protected audit path. Never call the Vikunja API directly.
Edit preserves fields you omit. Description may be cleared with --description "". Priority is 0 (none) through 5 (highest).
Only projects routed to this run's team are accessible. Task moves and deletion are not supported.
Do not retry a failed create or comment blindly: check Vikunja first, as the remote write might have succeeded before a connection failure.
Repeating Vikunja tasks may reschedule instead of staying complete; inspect the returned done value.
For code work without a checkout, use triagefactory exec workspace --help to select and materialize a repository.
`)
}

func parseCLI(args []string) (string, execArgs, error) {
	var a execArgs
	if len(args) == 0 {
		return "", a, fmt.Errorf("a Vikunja command is required")
	}
	method := args[0]
	rest := args[1:]
	allowed := map[string]bool{}
	switch method {
	case "list":
		allowed["--project"] = true
	case "create":
		allowed = map[string]bool{"--project": true, "--title": true, "--description": true, "--priority": true}
	case "edit", "view", "comments", "complete", "reopen", "comment":
		if len(rest) == 0 {
			return "", a, fmt.Errorf("a task ID is required")
		}
		id, err := strconv.ParseInt(rest[0], 10, 64)
		if err != nil || id <= 0 {
			return "", a, fmt.Errorf("use a positive numeric task ID")
		}
		a.ID, rest = id, rest[1:]
		if method == "edit" {
			allowed = map[string]bool{"--title": true, "--description": true, "--priority": true}
		} else if method == "comment" {
			allowed["--body"] = true
		}
	default:
		return "", a, fmt.Errorf("unknown Vikunja command %q", method)
	}
	seen := map[string]bool{}
	for len(rest) > 0 {
		flag := rest[0]
		if !allowed[flag] || seen[flag] || len(rest) < 2 {
			return "", a, fmt.Errorf("unknown, duplicate, or missing value for flag %q", flag)
		}
		seen[flag] = true
		value := rest[1]
		rest = rest[2:]
		switch flag {
		case "--project":
			id, err := strconv.ParseInt(value, 10, 64)
			if err != nil || id <= 0 {
				return "", a, fmt.Errorf("use a positive numeric project ID")
			}
			a.ProjectID = id
		case "--title":
			a.Title = &value
		case "--description":
			a.Description = &value
		case "--priority":
			priority, err := strconv.Atoi(value)
			if err != nil || priority < 0 || priority > 5 {
				return "", a, fmt.Errorf("priority must be from 0 to 5")
			}
			a.Priority = &priority
		case "--body":
			a.Body = value
		}
	}
	if (method == "list" || method == "create") && a.ProjectID == 0 {
		return "", a, fmt.Errorf("--project is required")
	}
	if method == "create" || method == "edit" {
		if err := a.TaskPatch.validate(method == "create"); err != nil {
			return "", a, err
		}
	}
	if method == "comment" && a.Body == "" {
		return "", a, fmt.Errorf("--body is required")
	}
	return method, a, nil
}

func runCLI(ctx context.Context, args []string, host agenthost.Client) int {
	if len(args) == 0 || execflags.HasHelpFlag(args, cliValueFlags) {
		fmt.Printf("Usage: %s vikunja <command> [flags]\n\n%s\n", prog.Prefix(), cliHelp)
		return 0
	}
	method, a, err := parseCLI(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	raw, err := json.Marshal(a)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	result, err := host.CallExtension(ctx, "vikunja", method, raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(string(result))
	return 0
}

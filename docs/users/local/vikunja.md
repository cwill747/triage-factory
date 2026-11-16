# Vikunja

Vikunja is a task location with polling and audited agent read/write commands.
It requires Vikunja 2.4.0 or later, with [API v2](https://vikunja.io/docs/api-v2/),
and an API token. It does not require an Enterprise entitlement.

## Connect

1. In your organization's settings, open **Vikunja**.
2. Enter the installation's public base URL, for example
   `https://tasks.example.com`. Do not append `/api/v2`.
3. Enter an API token. Grant the token permission to read projects and tasks,
   create and update tasks, and read and create task comments. Restrict the
   token's account/project access in Vikunja wherever possible.
4. Add numeric project IDs and select the Triage Factory team responsible for
   each project. Use IDs, not task indexes displayed within a project.
5. Choose a polling interval (30–86400 seconds; default 60), then connect.
6. Configure event handlers for the routed team's Vikunja events. Connecting
   alone does not create Triage Factory tasks without a matching handler.

Configuration requires an organization administrator. The token is stored in
Triage Factory's secret store and never returned by the settings API. Leave
its field blank when saving to preserve it. To change installations, disconnect
first. Disconnecting preserves existing tasks and history.

## Events and lifecycle

Polling emits `vikunja:task:available` for newly discovered open tasks,
`vikunja:task:updated` for title, description, priority, or project changes on
open tasks, and `vikunja:task:completed` when a task becomes done. Handlers can
filter by project ID and priority. Completion closes related Triage Factory
work through the usual event-routing machinery.

Already-completed tasks establish a quiet baseline. A previously closed entity
that is reopened becomes active without creating historical work; later changes
emit events normally. Reopening is therefore not a fresh task-available trigger
after the original completion has settled.

Removing a project stops new discovery in it. Previously known active tasks
continue to be refreshed so their eventual completion can close existing work.
Missing tasks or failed reads never imply completion. Turning the Vikunja event
source off suspends polling and agent access while retaining configuration.
Re-enabling establishes a quiet baseline for previously known tasks; changes
made during the pause do not trigger new work. Tasks completed during the pause
still close their existing Triage Factory tasks without triggering completion
automation. A failed task or project read does not prevent other tasks and
projects from being refreshed.

## Agent commands

Agents use the credential-protected CLI, not raw API tokens:

```text
triagefactory exec vikunja list --project 7
triagefactory exec vikunja view 42
triagefactory exec vikunja comments 42
triagefactory exec vikunja create --project 7 --title "Investigate failure"
triagefactory exec vikunja edit 42 --description "Updated details" --priority 3
triagefactory exec vikunja complete 42
triagefactory exec vikunja reopen 42
triagefactory exec vikunja comment 42 --body "Investigation complete"
```

Inside a native sandbox the applet is `tfac` rather than `triagefactory`.
`exec vikunja --help` documents the available flags. Commands return JSON.

A run can access only projects routed to its team, with live routing checks on
each command. Writes are recorded as external actions with the conversation,
organization credential, installation scope, and task/comment identity. Moving
and deleting tasks are not supported.

Edits preserve fields omitted by the caller; `--description ""` clears a
description and `--priority 0` clears priority. Updates use API v2 partial
updates and send only the fields you change; omitted fields are not resent.
Complete/reopen follow Vikunja's repeating-task behavior.
After a timeout, connection failure, or invalid write response, verify the task
or comments before retrying: the upstream write may already have succeeded.

Connection setup validates read access, not every write capability. A token
without a required write permission will fail that command at Vikunja. Use a
test project to verify the permissions before enabling automated writes.

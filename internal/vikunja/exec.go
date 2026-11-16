package vikunja

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/sky-ai-eng/triage-factory/cmd/exec/agenthost"
	"github.com/sky-ai-eng/triage-factory/internal/db"
	"github.com/sky-ai-eng/triage-factory/internal/domain"
	"github.com/sky-ai-eng/triage-factory/internal/entityscope"
	"github.com/sky-ai-eng/triage-factory/internal/eventing/eventsource"
)

type execArgs struct {
	ID        int64 `json:"id,omitempty"`
	ProjectID int64 `json:"project_id,omitempty"`
	TaskPatch
	Body string `json:"body,omitempty"`
}

type projectAuthorization struct {
	BaseURL      string `json:"base_url"`
	CredentialID string `json:"credential_id"`
}

func credentialID(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func init() {
	agenthost.RegisterExtension("vikunja", "", handleExec)
	agenthost.RegisterProviderCredential("vikunja", providerCredential)
	agenthost.RegisterProviderOp("vikunja", "authorize_project", authorizeProject)
	entityscope.RegisterScopeResolver("vikunja", func(ctx context.Context, stores db.Stores, org string) (string, error) {
		c, err := loadConnection(ctx, stores, org)
		return c.BaseURL, err
	})
}

func loadConnection(ctx context.Context, stores db.Stores, org string) (Connection, error) {
	raw, err := stores.Secrets.GetSystem(ctx, org, connectionKey)
	if err != nil {
		return Connection{}, err
	}
	c, err := decodeConnection(raw)
	if err != nil || c.Token == "" {
		return c, err
	}
	c.Projects, err = stores.VikunjaProjects.ListSystem(ctx, org, c.BaseURL)
	return c, err
}

func teamRoutes(ctx context.Context, stores db.Stores, org, teamID string) (Connection, error) {
	off, err := eventsource.Disabled(ctx, stores.OrgEventSources, org, "vikunja")
	if err != nil {
		return Connection{}, err
	}
	if off {
		return Connection{}, eventsource.ErrDisabled
	}
	c, err := loadConnection(ctx, stores, org)
	if err != nil || c.Token == "" || c.BaseURL == "" || teamID == "" {
		return Connection{}, err
	}
	team, err := stores.Teams.GetSystem(ctx, org, teamID)
	if err != nil {
		return Connection{}, err
	}
	if team == nil || team.DeletedAt != nil {
		return Connection{}, fmt.Errorf("Vikunja: an active team is required")
	}
	var projects []ProjectRoute
	for _, p := range c.Projects {
		if p.TeamID == teamID {
			projects = append(projects, p)
		}
	}
	c.Projects = projects
	return c, nil
}

func providerCredential(ctx context.Context, stores db.Stores, scope agenthost.ProvisionScope) (json.RawMessage, error) {
	c, err := teamRoutes(ctx, stores, scope.OrgID, scope.TeamID)
	if err != nil {
		return nil, err
	}
	if c.Token == "" || c.BaseURL == "" || len(c.Projects) == 0 {
		return nil, nil
	}
	return json.Marshal(c)
}

func authorizeProject(ctx context.Context, stores db.Stores, info agenthost.ConversationInfo, args json.RawMessage) (any, error) {
	var a execArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid Vikunja authorization arguments")
	}
	c, err := teamRoutes(ctx, stores, info.OrgID, info.TeamID)
	if err != nil {
		return nil, err
	}
	for _, p := range c.Projects {
		if (a.ProjectID == 0 || p.ProjectID == a.ProjectID) && c.Token != "" {
			return projectAuthorization{BaseURL: c.BaseURL, CredentialID: credentialID(c.Token)}, nil
		}
	}
	return nil, fmt.Errorf("Vikunja project is not routed to this team")
}

func handleExec(ctx context.Context, rt agenthost.ExtensionRuntime, method string, raw json.RawMessage) (json.RawMessage, error) {
	var a execArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("invalid Vikunja arguments")
	}
	switch method {
	case "view", "comments", "edit", "complete", "reopen", "comment":
		if a.ID <= 0 {
			return nil, fmt.Errorf("a positive task ID is required")
		}
	case "list", "create":
		if a.ProjectID <= 0 {
			return nil, fmt.Errorf("a positive project ID is required")
		}
	default:
		return nil, fmt.Errorf("unknown Vikunja method %q", method)
	}
	if method == "edit" || method == "create" {
		if err := a.TaskPatch.validate(method == "create"); err != nil {
			return nil, err
		}
	}
	if method == "comment" && strings.TrimSpace(a.Body) == "" {
		return nil, fmt.Errorf("a non-empty comment body is required")
	}
	credential, err := rt.ProviderCredential(ctx, "vikunja")
	if err != nil {
		return nil, err
	}
	var c Connection
	if err := json.Unmarshal(credential, &c); err != nil || c.Token == "" {
		return nil, fmt.Errorf("Vikunja is not configured for this run")
	}
	client, err := NewClient(c.BaseURL, c.Token)
	if err != nil {
		return nil, err
	}
	// Check credential freshness before sending even a read to the installation.
	// Task lookup is host-only; its project is checked before any data is returned.
	var current projectAuthorization
	if err := rt.Relay(ctx, "vikunja", "authorize_project", execArgs{}, &current); err != nil {
		return nil, err
	}
	if current.BaseURL != c.BaseURL || current.CredentialID != credentialID(c.Token) {
		return nil, fmt.Errorf("Vikunja connection changed; start a new run")
	}
	var task Task
	var document json.RawMessage
	if a.ID > 0 {
		task, document, err = client.taskDocument(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		a.ProjectID = task.ProjectID
	}
	// Both the sealed set and live org policy must admit the project. A
	// reconnected installation must never spend an old run's token.
	sealed := false
	for _, p := range c.Projects {
		if p.ProjectID == a.ProjectID && p.TeamID == rt.Info().TeamID {
			sealed = true
		}
	}
	if !sealed {
		return nil, fmt.Errorf("Vikunja project is outside this run's credential scope")
	}
	var auth projectAuthorization
	if err := rt.Relay(ctx, "vikunja", "authorize_project", execArgs{ProjectID: a.ProjectID}, &auth); err != nil {
		return nil, err
	}
	if auth.BaseURL != c.BaseURL || auth.CredentialID != credentialID(c.Token) {
		return nil, fmt.Errorf("Vikunja installation changed; start a new run")
	}
	if method == "list" {
		tasks, err := client.Tasks(ctx, a.ProjectID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(tasks)
	}
	id := strconv.FormatInt(task.ID, 10)
	url := c.BaseURL + "/tasks/" + id
	if method == "view" || method == "comments" {
		var result any = document
		if method == "comments" {
			comments, err := client.comments(ctx, task.ID)
			if err != nil {
				return nil, err
			}
			result = comments
		}
		rt.RecordReadTouch(ctx, "vikunja", id, id, domain.EntityContainer{}, domain.GitHubIdentity{}, url)
		return json.Marshal(result)
	}
	before := task.Done
	action := domain.ActionIssueUpdated
	kind := domain.ObjectKindIssue
	var result any
	anchor := ""
	switch method {
	case "comment":
		comment, err := client.comment(ctx, task.ID, a.Body)
		if err != nil {
			return nil, err
		}
		result = comment
		action, kind = domain.ActionCommentPosted, domain.ObjectKindComment
		if comment.ID > 0 {
			anchor = strconv.FormatInt(comment.ID, 10)
		}
	case "create":
		task, err = client.createTask(ctx, a.ProjectID, a.TaskPatch)
		action = domain.ActionIssueCreated
	default:
		patch := a.TaskPatch
		if method == "complete" || method == "reopen" {
			done := method == "complete"
			patch = TaskPatch{Done: &done}
		}
		task, err = client.updateTask(ctx, task, patch)
		if err == nil && before != task.Done {
			if task.Done {
				action = domain.ActionIssueClosed
			} else {
				action = domain.ActionIssueReopened
			}
		}
	}
	if err != nil {
		return nil, err
	}
	id = strconv.FormatInt(task.ID, 10)
	url = c.BaseURL + "/tasks/" + id
	act := domain.ExternalAction{Provider: domain.ProviderVikunja, Action: action, Target: id, ExternalID: id, URL: url, Credential: domain.CredentialVikunjaOrg}
	if method != "comment" {
		result = task
		act.FromState, act.ToState = strconv.FormatBool(before), strconv.FormatBool(task.Done)
	}
	if task.ID > 0 && (method != "comment" || anchor != "") {
		act.SetObject(kind, domain.ObjectKey("vikunja", kind, domain.EntityResource("vikunja", c.BaseURL, id), anchor))
	}
	rt.Record(ctx, nil, &act, domain.EntityContainer{}, domain.GitHubIdentity{})
	return json.Marshal(result)
}

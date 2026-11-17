package vikunja

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sky-ai-eng/triage-factory/cmd/exec/agenthost"
	"github.com/sky-ai-eng/triage-factory/internal/db"
	"github.com/sky-ai-eng/triage-factory/internal/domain"
)

type execRuntime struct {
	agenthost.ExtensionRuntime
	connection Connection
	auth       projectAuthorization
	denied     bool
	actions    []domain.ExternalAction
	touches    int
}

func (r *execRuntime) Info() agenthost.ConversationInfo {
	return agenthost.ConversationInfo{OrgID: "org", TeamID: "team", ConversationID: "conversation"}
}
func (r *execRuntime) ProviderCredential(context.Context, string) (json.RawMessage, error) {
	return json.Marshal(r.connection)
}
func (r *execRuntime) Relay(_ context.Context, namespace, op string, args, out any) error {
	if namespace != "vikunja" || op != "authorize_project" || r.denied {
		return fmt.Errorf("denied")
	}
	*(out.(*projectAuthorization)) = r.auth
	return nil
}
func (r *execRuntime) Record(_ context.Context, _ *domain.Deliverable, a *domain.ExternalAction, _ domain.EntityContainer, _ domain.GitHubIdentity) {
	r.actions = append(r.actions, *a)
}
func (r *execRuntime) RecordReadTouch(context.Context, string, string, string, domain.EntityContainer, domain.GitHubIdentity, string) {
	r.touches++
}

func newExecRuntime(base string) *execRuntime {
	return &execRuntime{connection: Connection{BaseURL: base, Token: "secret", Projects: []ProjectRoute{{ProjectID: 7, TeamID: "team"}}}, auth: projectAuthorization{BaseURL: base, CredentialID: credentialID("secret")}}
}

func TestExecWritesPreserveTaskAndRecord(t *testing.T) {
	for _, method := range []string{"edit", "complete", "reopen", "create", "comment"} {
		t.Run(method, func(t *testing.T) {
			writes := 0
			var payload map[string]json.RawMessage
			var stored map[string]json.RawMessage
			initial := fmt.Sprintf(`{"id":42,"project_id":7,"title":"original","description":"keep","priority":3,"done":%t,"due_date":"2030-01-01T00:00:00Z","repeat_after":86400,"reminders":[{"reminder":"2030-01-01T00:00:00Z"}],"assignees":[{"id":9}],"labels":[{"id":8}],"hex_color":"ff0000","percent_done":0.5,"future_field":"keep too"}`, method == "reopen")
			if err := json.Unmarshal([]byte(initial), &stored); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer secret" {
					t.Error("missing bearer credential")
				}
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					if r.URL.Path != "/api/v2/tasks/42" {
						t.Errorf("path = %s", r.URL.Path)
					}
					_ = json.NewEncoder(w).Encode(stored)
					// Another writer changes an omitted field after the authorization read.
					stored["future_field"] = json.RawMessage(`"concurrent change"`)
					return
				}
				writes++
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if method == "create" || method == "comment" {
					if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
						t.Errorf("create request = %s %s", r.Method, r.Header.Get("Content-Type"))
					}
					w.WriteHeader(http.StatusCreated)
				} else if r.Method != http.MethodPatch || r.URL.Path != "/api/v2/tasks/42" || r.Header.Get("Content-Type") != "application/merge-patch+json" {
					t.Errorf("update request = %s %s %s", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
				}
				if method == "comment" {
					if r.URL.Path != "/api/v2/tasks/42/comments" {
						t.Errorf("path = %s", r.URL.Path)
					}
					fmt.Fprint(w, `{"id":91,"comment":"hello"}`)
					return
				}
				if method == "create" {
					if r.URL.Path != "/api/v2/projects/7/tasks" {
						t.Errorf("path = %s", r.URL.Path)
					}
					stored["id"] = json.RawMessage(`43`)
				}
				for name, value := range payload {
					stored[name] = value
				}
				_ = json.NewEncoder(w).Encode(stored)
			}))
			defer server.Close()
			rt := newExecRuntime(server.URL)
			args := `{"id":42}`
			switch method {
			case "edit":
				args = `{"id":42,"title":"changed","description":"","priority":0}`
			case "create":
				args = `{"project_id":7,"title":"new"}`
			case "comment":
				args = `{"id":42,"body":"hello"}`
			}
			if _, err := handleExec(t.Context(), rt, method, json.RawMessage(args)); err != nil {
				t.Fatal(err)
			}
			if writes != 1 || len(rt.actions) != 1 {
				t.Fatalf("writes=%d actions=%d", writes, len(rt.actions))
			}
			wantBody := map[string]string{
				"edit":     `{"title":"changed","description":"","priority":0}`,
				"create":   `{"title":"new"}`,
				"comment":  `{"comment":"hello"}`,
				"complete": `{"done":true}`,
				"reopen":   `{"done":false}`,
			}[method]
			var wantPayload map[string]json.RawMessage
			if err := json.Unmarshal([]byte(wantBody), &wantPayload); err != nil {
				t.Fatal(err)
			}
			if len(payload) != len(wantPayload) {
				t.Fatalf("payload=%v; want only %s", payload, wantBody)
			}
			for name, value := range wantPayload {
				if string(payload[name]) != string(value) {
					t.Errorf("payload field %s=%s, want %s", name, payload[name], value)
				}
			}
			act := rt.actions[0]
			if act.Provider != "vikunja" || act.Credential != domain.CredentialVikunjaOrg || act.ObjectKey == "" || act.URL == "" {
				t.Fatalf("incomplete audit action: %+v", act)
			}
			wantAction := map[string]string{"edit": domain.ActionIssueUpdated, "create": domain.ActionIssueCreated, "complete": domain.ActionIssueClosed, "reopen": domain.ActionIssueReopened, "comment": domain.ActionCommentPosted}[method]
			if act.Action != wantAction {
				t.Errorf("action = %s; want %s", act.Action, wantAction)
			}
			if method == "comment" {
				if !strings.HasSuffix(act.ObjectKey, ":91") {
					t.Errorf("comment key = %s", act.ObjectKey)
				}
			} else if method != "create" {
				for _, name := range []string{"due_date", "repeat_after", "reminders", "assignees", "labels", "hex_color", "percent_done", "future_field"} {
					if len(stored[name]) == 0 || len(payload[name]) != 0 {
						t.Errorf("omitted field %s was lost or sent in patch", name)
					}
				}
				if string(stored["future_field"]) != `"concurrent change"` {
					t.Error("update overwrote a concurrent change to an omitted field")
				}
				if method == "edit" && (string(payload["priority"]) != "0" || string(payload["description"]) != `""`) {
					t.Errorf("explicit clearing was lost: %v", payload)
				}
				if method != "edit" && string(stored["description"]) != `"keep"` {
					t.Error("completion erased description")
				}
			}
		})
	}
}

func TestExecViewPreservesFullTaskDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/tasks/42" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"id":42,"project_id":7,"title":"Task","future_field":{"nested":"context"}}`)
	}))
	defer server.Close()
	rt := newExecRuntime(server.URL)
	raw, err := handleExec(t.Context(), rt, "view", json.RawMessage(`{"id":42}`))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil || string(document["future_field"]) != `{"nested":"context"}` {
		t.Fatalf("view lost task context: %s, %v", raw, err)
	}
	if len(rt.actions) != 0 {
		t.Fatal("view recorded a write")
	}
}

func TestExecRefusesUnauthorizedOrFailedWrites(t *testing.T) {
	for _, scenario := range []string{"other project", "other team", "live policy removed", "installation changed", "token rotated", "upstream rejected"} {
		t.Run(scenario, func(t *testing.T) {
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					fmt.Fprint(w, `{"id":42,"project_id":7,"title":"original"}`)
					return
				}
				writes++
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, "secret must not escape")
			}))
			defer server.Close()
			rt := newExecRuntime(server.URL)
			switch scenario {
			case "other project":
				rt.connection.Projects[0].ProjectID = 8
			case "other team":
				rt.connection.Projects[0].TeamID = "other"
			case "live policy removed":
				rt.denied = true
			case "installation changed":
				rt.auth.BaseURL = "https://other.example"
			case "token rotated":
				rt.auth.CredentialID = credentialID("replacement")
			}
			_, err := handleExec(t.Context(), rt, "complete", json.RawMessage(`{"id":42}`))
			if err == nil || strings.Contains(err.Error(), "secret") || len(rt.actions) > 0 {
				t.Fatalf("err=%v actions=%d", err, len(rt.actions))
			}
			if scenario != "upstream rejected" && writes > 0 {
				t.Fatal("unauthorized request reached upstream write")
			}
		})
	}
}

func TestCLIValidation(t *testing.T) {
	for _, args := range [][]string{{"edit", "42"}, {"create", "--project", "7"}, {"view", "-1"}, {"view", "42", "--body", "oops"}, {"edit", "42", "--priority", "6"}, {"comment", "42"}, {"list"}, {"create", "--project", "7", "--title", "x", "--title", "y"}} {
		if _, _, err := parseCLI(args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
	method, args, err := parseCLI([]string{"edit", "42", "--description", "", "--priority", "0"})
	if err != nil || method != "edit" || args.Description == nil || *args.Description != "" || args.Priority == nil || *args.Priority != 0 {
		t.Fatalf("clearing fields: %+v %v", args, err)
	}
	if runCLI(t.Context(), []string{"edit", "--help"}, nil) != 0 {
		t.Fatal("help must not require a host")
	}
}

type execSecrets struct {
	db.SecretStore
	value string
}

func (s execSecrets) GetSystem(context.Context, string, string) (string, error) { return s.value, nil }

type execPolicy struct {
	db.OrgEventSourceStore
	disabled bool
}

func (p execPolicy) ListDisabledSystem(context.Context, string) ([]string, error) {
	if p.disabled {
		return []string{"vikunja"}, nil
	}
	return nil, nil
}

type execTeams struct{ db.TeamsStore }

func (execTeams) GetSystem(context.Context, string, string) (*domain.Team, error) {
	return &domain.Team{ID: "team"}, nil
}

type execProjectRoutes struct {
	db.VikunjaProjectStore
	routes []db.VikunjaProjectRoute
}

func (s execProjectRoutes) ListSystem(context.Context, string, string) ([]db.VikunjaProjectRoute, error) {
	return s.routes, nil
}

func TestCredentialAndLiveProjectPolicy(t *testing.T) {
	connection := Connection{BaseURL: "https://tasks.example", Token: "secret", Projects: []ProjectRoute{{ProjectID: 7, TeamID: "team"}, {ProjectID: 8, TeamID: "other"}}}
	raw, _ := json.Marshal(connection)
	stores := db.Stores{Secrets: execSecrets{value: string(raw)}, Teams: execTeams{}, OrgEventSources: execPolicy{}, VikunjaProjects: execProjectRoutes{routes: connection.Projects}}
	credential, err := providerCredential(t.Context(), stores, agenthost.ProvisionScope{OrgID: "org", TeamID: "team"})
	if err != nil {
		t.Fatal(err)
	}
	var sealed Connection
	if err := json.Unmarshal(credential, &sealed); err != nil || len(sealed.Projects) != 1 || sealed.Projects[0].ProjectID != 7 {
		t.Fatalf("sealed=%+v err=%v", sealed, err)
	}
	for _, id := range []int64{7, 8} {
		args, _ := json.Marshal(execArgs{ProjectID: id})
		result, err := authorizeProject(t.Context(), stores, agenthost.ConversationInfo{OrgID: "org", TeamID: "team"}, args)
		if id == 7 && (err != nil || strings.Contains(fmt.Sprint(result), "secret")) {
			t.Fatalf("auth=%v err=%v", result, err)
		}
		if id == 8 && err == nil {
			t.Fatal("authorized other team's project")
		}
	}
	stores.VikunjaProjects = execProjectRoutes{}
	if _, err := authorizeProject(t.Context(), stores, agenthost.ConversationInfo{OrgID: "org", TeamID: "team"}, json.RawMessage(`{"project_id":7}`)); err == nil {
		t.Fatal("credential envelope's stale routes authorized a removed project")
	}
	stores.OrgEventSources = execPolicy{disabled: true}
	if _, err := providerCredential(t.Context(), stores, agenthost.ProvisionScope{OrgID: "org", TeamID: "team"}); err == nil {
		t.Fatal("paused source resolved credentials")
	}
	stores.OrgEventSources = execPolicy{}
	stores.Secrets = execSecrets{}
	credential, err = providerCredential(t.Context(), stores, agenthost.ProvisionScope{OrgID: "org", TeamID: "team"})
	if err != nil || credential != nil {
		t.Fatalf("unconfigured provider: %s %v", credential, err)
	}
}

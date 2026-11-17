package vikunja

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLiveReadWrite creates and deletes one task in an explicitly selected
// existing project. The token is read from a file, never logged.
func TestLiveReadWrite(t *testing.T) {
	base, keyFile := os.Getenv("VIKUNJA_TEST_URL"), os.Getenv("VIKUNJA_TEST_KEY_FILE")
	projectName := os.Getenv("VIKUNJA_TEST_PROJECT")
	if base == "" || keyFile == "" || projectName == "" {
		t.Skip("set VIKUNJA_TEST_URL, VIKUNJA_TEST_KEY_FILE, and VIKUNJA_TEST_PROJECT for live writes")
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal("could not read live test key file")
	}
	token := strings.TrimSpace(string(key))
	if len(token) >= 7 && strings.EqualFold(token[:7], "Bearer ") {
		token = strings.TrimSpace(token[7:])
	}
	client, err := NewClient(base, token)
	if err != nil {
		t.Fatal("could not initialize live client")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	var project Project
	for page := 1; page <= 10000; page++ {
		projects, err := readPage[Project](ctx, client, "/projects", page, nil)
		if err != nil {
			t.Fatalf("find existing project: %v", err)
		}
		for _, p := range projects.Items {
			if strings.EqualFold(p.Title, projectName) {
				if project.ID != 0 {
					t.Fatal("project name is ambiguous")
				}
				project = p
			}
		}
		if page >= projects.TotalPages {
			break
		}
		if page == 10000 {
			t.Fatal("project pagination limit exceeded")
		}
	}
	if project.ID <= 0 {
		t.Fatal("requested existing project not found")
	}
	t.Logf("using existing project %s (%d)", projectName, project.ID)
	if _, err := client.Project(ctx, project.ID); err != nil {
		t.Fatalf("read project: %v", err)
	}
	rt := newExecRuntime(client.baseURL)
	rt.connection.Token = client.token
	rt.connection.Projects[0].ProjectID = project.ID
	rt.auth.CredentialID = credentialID(client.token)
	call := func(method string, args any) json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal("could not encode test arguments")
		}
		result, err := handleExec(ctx, rt, method, raw)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		t.Logf("%s succeeded", method)
		return result
	}
	raw := call("create", map[string]any{"project_id": project.ID, "title": fmt.Sprintf("Triage Factory temporary integration test %d", time.Now().UnixNano()), "description": "preserve this description", "priority": 3})
	var task Task
	if err := json.Unmarshal(raw, &task); err != nil || task.ID <= 0 {
		t.Fatal("invalid created task")
	}
	taskID := task.ID
	t.Logf("created test task %d", taskID)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := client.request(cleanupCtx, http.MethodDelete, "/tasks/"+strconv.FormatInt(taskID, 10), nil, nil); err != nil {
			t.Errorf("cleanup failed for test task %d: %v", taskID, err)
		} else {
			t.Logf("deleted test task %d", taskID)
		}
	})
	read := func() Task {
		t.Helper()
		task, err := client.Task(ctx, taskID)
		if err != nil {
			t.Fatalf("verify task: %v", err)
		}
		return task
	}
	raw = call("list", map[string]any{"project_id": project.ID})
	var tasks []Task
	if err := json.Unmarshal(raw, &tasks); err != nil {
		t.Fatal("invalid task list")
	}
	found := false
	for _, listed := range tasks {
		if listed.ID == taskID {
			found = true
		}
	}
	if !found {
		t.Fatal("created task missing from project polling")
	}
	call("view", map[string]any{"id": taskID})
	call("edit", map[string]any{"id": taskID, "title": "Edited integration task"})
	task = read()
	if task.Title != "Edited integration task" || task.Description != "preserve this description" || task.Priority != 3 {
		t.Fatal("edit lost supplied or omitted fields")
	}
	call("complete", map[string]any{"id": taskID})
	if task = read(); !task.Done || task.Priority != 3 || task.Description != "preserve this description" {
		t.Fatal("completion failed or erased fields")
	}
	call("reopen", map[string]any{"id": taskID})
	if task = read(); task.Done || task.Priority != 3 || task.Description != "preserve this description" {
		t.Fatal("reopening failed or erased fields")
	}
	call("edit", map[string]any{"id": taskID, "description": "", "priority": 0})
	if task = read(); task.Description != "" || task.Priority != 0 {
		t.Fatal("explicit zero/empty edits failed")
	}
	call("comment", map[string]any{"id": taskID, "body": "Temporary integration test comment"})
	raw = call("comments", map[string]any{"id": taskID})
	var comments []Comment
	if err := json.Unmarshal(raw, &comments); err != nil || len(comments) != 1 || comments[0].Comment != "Temporary integration test comment" {
		t.Fatal("comment could not be read back")
	}
	if len(rt.actions) != 6 {
		t.Fatalf("expected six recorded writes, got %d", len(rt.actions))
	}
	t.Log("verified create/list/view/edit/complete/reopen/comments and six runtime audit records")
}

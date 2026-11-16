package vikunja

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// TaskPatch distinguishes omitted fields from explicit zero/empty values in
// API v2's JSON Merge Patch requests.
type TaskPatch struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	Priority    *int    `json:"priority,omitempty"`
	Done        *bool   `json:"done,omitempty"`
}

func (p TaskPatch) validate(create bool) error {
	if create && p.Title == nil || p.Title != nil && strings.TrimSpace(*p.Title) == "" {
		return fmt.Errorf("a non-empty title is required")
	}
	if p.Priority != nil && (*p.Priority < 0 || *p.Priority > 5) {
		return fmt.Errorf("priority must be from 0 to 5")
	}
	if !create && p == (TaskPatch{}) {
		return fmt.Errorf("provide at least one field to edit")
	}
	return nil
}

func (c *Client) taskDocument(ctx context.Context, id int64) (Task, json.RawMessage, error) {
	var document json.RawMessage
	if _, err := c.get(ctx, "/tasks/"+strconv.FormatInt(id, 10), &document); err != nil {
		return Task{}, nil, err
	}
	var task Task
	if err := json.Unmarshal(document, &task); err != nil || task.ID != id || task.ProjectID <= 0 {
		return Task{}, nil, fmt.Errorf("invalid Vikunja task identity")
	}
	return task, document, nil
}

func (c *Client) createTask(ctx context.Context, project int64, patch TaskPatch) (Task, error) {
	var result Task
	_, err := c.request(ctx, http.MethodPost, "/projects/"+strconv.FormatInt(project, 10)+"/tasks", patch, &result)
	if err == nil && (result.ID <= 0 || result.ProjectID != project) {
		err = fmt.Errorf("vikunja create returned an unexpected identity; verify the project before retrying")
	}
	return result, err
}

func (c *Client) updateTask(ctx context.Context, task Task, patch TaskPatch) (Task, error) {
	var result Task
	_, err := c.request(ctx, http.MethodPatch, "/tasks/"+strconv.FormatInt(task.ID, 10), patch, &result)
	if err == nil && (result.ID != task.ID || result.ProjectID != task.ProjectID) {
		err = fmt.Errorf("vikunja update returned an unexpected identity; verify the task before retrying")
	}
	return result, err
}

type Comment struct {
	ID      int64  `json:"id"`
	Comment string `json:"comment"`
}

func (c *Client) comments(ctx context.Context, id int64) ([]Comment, error) {
	result := []Comment{}
	seen := map[int64]bool{}
	for page := 1; page <= 10000; page++ {
		batch, err := readPage[Comment](ctx, c, fmt.Sprintf("/tasks/%d/comments", id), page, nil)
		if err != nil {
			return nil, err
		}
		for _, comment := range batch.Items {
			if comment.ID <= 0 || seen[comment.ID] {
				return nil, fmt.Errorf("invalid or repeated Vikunja comment identity")
			}
			seen[comment.ID] = true
			result = append(result, comment)
		}
		if page >= batch.TotalPages {
			return result, nil
		}
	}
	return nil, fmt.Errorf("vikunja comment pagination limit exceeded")
}

func (c *Client) comment(ctx context.Context, id int64, body string) (Comment, error) {
	var result Comment
	_, err := c.request(ctx, http.MethodPost, "/tasks/"+strconv.FormatInt(id, 10)+"/comments", map[string]string{"comment": body}, &result)
	if err == nil && result.ID <= 0 {
		err = fmt.Errorf("vikunja comment returned no identity; verify comments before retrying")
	}
	return result, err
}

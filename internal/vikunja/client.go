// Package vikunja integrates tasks from self-hosted Vikunja.
package vikunja

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Task struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"project_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Done        bool   `json:"done"`
	Priority    int    `json:"priority"`
	Updated     string `json:"updated"`
}

type Project struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// CanonicalURL accepts an installation root, not an API endpoint. Redirects
// are refused by the client so an upstream cannot redirect its bearer token.
func CanonicalURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", fmt.Errorf("use an http(s) installation URL without credentials, query or fragment")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	if strings.HasSuffix(u.Path, "/api/v1") || strings.HasSuffix(u.Path, "/api/v2") {
		return "", fmt.Errorf("use the installation URL without /api/v1 or /api/v2")
	}
	return u.String(), nil
}

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewClient(baseURL, token string) (*Client, error) {
	base, err := CanonicalURL(baseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("an API token is required")
	}
	return &Client{baseURL: base, token: token, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) get(ctx context.Context, path string, out any) (http.Header, error) {
	return c.request(ctx, http.MethodGet, path, nil, out)
}

func (c *Client) request(ctx context.Context, method, path string, payload any, out any) (http.Header, error) {
	var bodyReader io.Reader
	if payload != nil {
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/api/v2"+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		contentType := "application/json"
		if method == http.MethodPatch {
			contentType = "application/merge-patch+json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vikunja request failed: %w", err)
	}
	defer resp.Body.Close()
	// Do not echo response bodies: reverse proxies can include credentials.
	if resp.StatusCode == http.StatusNoContent && method == http.MethodDelete && out == nil {
		return resp.Header, nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("vikunja returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 8<<20 {
		return nil, fmt.Errorf("vikunja response exceeds 8 MiB")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return nil, fmt.Errorf("invalid Vikunja JSON response")
	}
	return resp.Header, nil
}

func (c *Client) Project(ctx context.Context, id int64) (Project, error) {
	var p Project
	_, err := c.get(ctx, "/projects/"+strconv.FormatInt(id, 10), &p)
	if err == nil && p.ID != id {
		err = fmt.Errorf("vikunja returned a different project")
	}
	return p, err
}

func (c *Client) Task(ctx context.Context, id int64) (Task, error) {
	var t Task
	_, err := c.get(ctx, "/tasks/"+strconv.FormatInt(id, 10), &t)
	if err == nil && t.ID != id {
		err = fmt.Errorf("vikunja returned a different task")
	}
	return t, err
}

func (c *Client) Tasks(ctx context.Context, projectID int64) ([]Task, error) {
	tasks := []Task{}
	seen := map[int64]bool{}
	for page := 1; page <= 10000; page++ {
		q := url.Values{"sort_by": {"id"}, "order_by": {"asc"}}
		batch, err := readPage[Task](ctx, c, "/projects/"+strconv.FormatInt(projectID, 10)+"/tasks", page, q)
		if err != nil {
			return nil, err
		}
		for _, t := range batch.Items {
			if t.ID <= 0 || t.ProjectID != projectID {
				return nil, fmt.Errorf("vikunja returned a task outside the requested project")
			}
			if seen[t.ID] {
				return nil, fmt.Errorf("vikunja pagination repeated a task; retry on the next poll")
			}
			seen[t.ID] = true
			tasks = append(tasks, t)
		}
		if page >= batch.TotalPages {
			return tasks, nil
		}
	}
	return nil, fmt.Errorf("vikunja pagination limit exceeded")
}

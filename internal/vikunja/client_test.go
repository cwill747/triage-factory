package vikunja

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCanonicalURL(t *testing.T) {
	for _, raw := range []string{"", "ftp://example.com", "https://user:pass@example.com", "https://example.com?token=secret", "https://example.com/#fragment", "https://example.com/api/v1", "https://example.com/api/v2", "https://example.com/vikunja/api/v2/"} {
		if _, err := CanonicalURL(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	got, err := CanonicalURL(" HTTPS://TASKS.EXAMPLE.COM/vikunja/ ")
	if err != nil || got != "https://tasks.example.com/vikunja" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestClientReadsV2Resources(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method=%s", r.Method)
		}
		switch r.URL.Path {
		case "/api/v2/projects/7":
			fmt.Fprint(w, `{"id":7,"title":"Project","max_permission":2}`)
		case "/api/v2/tasks/42":
			fmt.Fprint(w, `{"id":42,"project_id":7,"title":"Task"}`)
		default:
			t.Errorf("unexpected path=%s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	c, _ := NewClient(upstream.URL, "secret")
	if project, err := c.Project(t.Context(), 7); err != nil || project.ID != 7 {
		t.Fatalf("project=%+v err=%v", project, err)
	}
	if task, err := c.Task(t.Context(), 42); err != nil || task.ID != 42 || task.ProjectID != 7 {
		t.Fatalf("task=%+v err=%v", task, err)
	}
}

func TestTasksPagination(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/vikunja/api/v2/projects/7/tasks" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		q := r.URL.Query()
		if q.Get("per_page") != "100" || q.Get("sort_by") != "id" || q.Get("order_by") != "asc" {
			t.Errorf("unexpected list query %v", q)
		}
		// Only the body is authoritative, even if a proxy adds a stale v1 header.
		w.Header().Set("X-Pagination-Total-Pages", "1")
		fmt.Fprintf(w, `{"items":[{"id":%s,"project_id":7,"title":"Task","done":false}],"total":2,"page":%s,"per_page":1,"total_pages":2}`, q.Get("page"), q.Get("page"))
	}))
	defer upstream.Close()
	c, _ := NewClient(upstream.URL+"/vikunja", "secret")
	tasks, err := c.Tasks(t.Context(), 7)
	if err != nil || len(tasks) != 2 || calls != 2 {
		t.Fatalf("tasks=%v calls=%d err=%v", tasks, calls, err)
	}
}

func TestTasksEmptyPage(t *testing.T) {
	for _, items := range []string{"[]", "null"} {
		t.Run(items, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"items":%s,"total":0,"page":1,"per_page":100,"total_pages":0}`, items)
			}))
			defer upstream.Close()
			c, _ := NewClient(upstream.URL, "secret")
			tasks, err := c.Tasks(t.Context(), 7)
			if err != nil || len(tasks) != 0 {
				t.Fatalf("tasks=%v err=%v", tasks, err)
			}
		})
	}
}

func TestClientRefusesRedirectsAndRedactsErrors(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer upstream.Close()
	c, _ := NewClient(upstream.URL, "secret")
	if _, err := c.Tasks(t.Context(), 7); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unexpected error: %v", err)
	}
	if leaked {
		t.Fatal("followed redirect")
	}
}

func TestTasksRejectWrongProjectAndRepeatedPages(t *testing.T) {
	for _, project := range []int{7, 8} {
		t.Run(fmt.Sprint(project), func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				fmt.Fprintf(w, `{"items":[{"id":1,"project_id":%d}],"total":2,"page":%s,"per_page":1,"total_pages":2}`, project, r.URL.Query().Get("page"))
			}))
			defer upstream.Close()
			c, _ := NewClient(upstream.URL, "secret")
			if _, err := c.Tasks(t.Context(), 7); err == nil {
				t.Fatal("accepted invalid pagination or project")
			}
			wantCalls := 1
			if project == 7 {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("calls=%d, want %d", calls, wantCalls)
			}
		})
	}
}

func TestClientRejectsInvalidPagination(t *testing.T) {
	for name, body := range map[string]string{
		"bare array":               `[{"id":1,"project_id":7}]`,
		"missing metadata":         `{"items":[]}`,
		"inconsistent total pages": `{"items":[{"id":1,"project_id":7}],"total":2,"page":1,"per_page":1,"total_pages":1}`,
		"wrong page":               `{"items":[],"total":0,"page":2,"per_page":100,"total_pages":0}`,
		"zero page size":           `{"items":[],"total":0,"page":1,"per_page":0,"total_pages":0}`,
		"negative total":           `{"items":[],"total":-1,"page":1,"per_page":100,"total_pages":0}`,
		"negative pages":           `{"items":[],"total":0,"page":1,"per_page":100,"total_pages":-1}`,
		"empty unfinished page":    `{"items":[],"total":2,"page":1,"per_page":1,"total_pages":2}`,
		"nonempty zero pages":      `{"items":[{"id":1,"project_id":7}],"total":1,"page":1,"per_page":1,"total_pages":0}`,
		"too many items":           `{"items":[{"id":1,"project_id":7},{"id":2,"project_id":7}],"total":2,"page":1,"per_page":1,"total_pages":2}`,
	} {
		t.Run(name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer upstream.Close()
			c, _ := NewClient(upstream.URL, "secret")
			if _, err := c.Tasks(t.Context(), 7); err == nil {
				t.Fatal("accepted invalid pagination")
			}
		})
	}
}

func TestClientRejectsHTMLAndUpstreamBody(t *testing.T) {
	for _, status := range []int{200, 401, 422, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if status == http.StatusOK {
					fmt.Fprint(w, "<html>secret</html>")
					return
				}
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"status":422,"title":"Validation failed","detail":"secret"}`)
			}))
			defer upstream.Close()
			c, _ := NewClient(upstream.URL, "secret")
			if _, err := c.Tasks(t.Context(), 7); err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unexpected error %v", err)
			}
		})
	}
}

func TestClientDeleteAcceptsNoContent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/tasks/42" {
			t.Errorf("path=%s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	c, _ := NewClient(upstream.URL, "secret")
	if _, err := c.request(t.Context(), http.MethodDelete, "/tasks/42", nil, nil); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := c.Task(t.Context(), 42); err == nil {
		t.Fatal("accepted a bodyless task read")
	}
}

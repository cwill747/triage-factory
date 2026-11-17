package vikunja

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCommentsPagination(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v2/tasks/42/comments" || r.Method != http.MethodGet {
			t.Errorf("path=%s", r.URL.Path)
		}
		fmt.Fprintf(w, `{"items":[{"id":%s,"comment":"text"}],"page":%s,"per_page":1,"total":2,"total_pages":2}`, r.URL.Query().Get("page"), r.URL.Query().Get("page"))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	comments, err := client.comments(t.Context(), 42)
	if err != nil || len(comments) != 2 || requests != 2 {
		t.Fatalf("comments=%v requests=%d err=%v", comments, requests, err)
	}
}

package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPiSessionPath(t *testing.T) {
	for _, root := range []string{t.TempDir(), t.TempDir()} {
		got, err := ClaudeSessionPath(root, "pi:session.jsonl")
		want := filepath.Join(root, "_tfac", "pi", "sessions", "session.jsonl")
		if err != nil || got != want {
			t.Fatalf("path = %q, %v; want %q", got, err, want)
		}
		if err := os.MkdirAll(filepath.Dir(got), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(got, []byte("transcript\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if data, ok := ReadClaudeSessionTranscript(root, "pi:session.jsonl"); !ok || string(data) != "transcript\n" {
			t.Fatalf("read transcript = %q, %v", data, ok)
		}
	}
	for _, id := range []string{"pi:", "pi:../escape.jsonl", "pi:/tmp/escape.jsonl", `pi:..\escape.jsonl`, "pi:session"} {
		if _, err := ClaudeSessionPath(t.TempDir(), id); err == nil {
			t.Errorf("accepted %q", id)
		}
	}
}

package github

import (
	"strings"
	"testing"
)

// TestReassemblePRDiff_OversizedVsBinary verifies the 406-reassembly path
// distinguishes a genuine binary file ("Binary files differ") from an
// oversized text file whose patch GitHub omitted (a note, never a fabricated
// hunk or a wrong "binary" label).
func TestReassemblePRDiff_OversizedVsBinary(t *testing.T) {
	out := ReassemblePRDiff([]PRFile{
		{Filename: "image.png", Status: "added", Additions: 0, Deletions: 0},
		{Filename: "huge.go", Status: "modified", Additions: 5000, Deletions: 10},
	})
	if !strings.Contains(out, "Binary files a/image.png and b/image.png differ") {
		t.Errorf("expected binary marker for image.png; got:\n%s", out)
	}
	if !strings.Contains(out, "patch unavailable") || !strings.Contains(out, "+5000 -10") {
		t.Errorf("expected oversized-text note for huge.go; got:\n%s", out)
	}
	if strings.Contains(out, "Binary files a/huge.go") {
		t.Errorf("oversized text file must NOT be labeled binary; got:\n%s", out)
	}
}

// TestReassemblePRDiff_RenameNoPatch covers the empty-patch rename case: it must
// emit rename headers, not the misleading "patch unavailable (too large)" note
// (a rename with no content change isn't truncated), and never a binary line.
func TestReassemblePRDiff_RenameNoPatch(t *testing.T) {
	out := ReassemblePRDiff([]PRFile{
		{Filename: "new.png", PreviousFilename: "old.png", Status: "renamed", Additions: 0, Deletions: 0},
	})
	if !strings.Contains(out, "diff --git a/old.png b/new.png") {
		t.Errorf("expected rename diff header; got:\n%s", out)
	}
	if !strings.Contains(out, "rename from old.png") || !strings.Contains(out, "rename to new.png") {
		t.Errorf("expected rename from/to headers; got:\n%s", out)
	}
	if strings.Contains(out, "patch unavailable") {
		t.Errorf("rename must not be labeled patch-unavailable/too-large; got:\n%s", out)
	}
}

// TestSingleFileDiff covers the single-file 406 fallback: a synthesized
// single-file diff for a present path, and "" for an absent one.
func TestSingleFileDiff(t *testing.T) {
	files := []PRFile{
		{Filename: "a.go", Status: "modified", Additions: 1, Deletions: 0, Patch: "@@ -1 +1,2 @@\n x\n+y"},
		{Filename: "b.go", Status: "added", Additions: 1, Deletions: 0, Patch: "@@ -0,0 +1 @@\n+z"},
	}
	got := SingleFileDiff(files, "a.go")
	if !strings.Contains(got, "diff --git a/a.go b/a.go") || !strings.Contains(got, "+y") {
		t.Errorf("expected a.go diff; got:\n%s", got)
	}
	if strings.Contains(got, "b.go") {
		t.Errorf("single-file diff for a.go must not include b.go; got:\n%s", got)
	}
	if SingleFileDiff(files, "missing.go") != "" {
		t.Error("expected empty string for a path not in the file list")
	}
}

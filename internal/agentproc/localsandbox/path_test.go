package localsandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVisiblePath(t *testing.T) {
	root := t.TempDir()
	store := filepath.Join(root, "store", "bin")
	profile := filepath.Join(root, "profile")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(store), profile); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(profile, "bin") + string(filepath.ListSeparator) + "relative" + string(filepath.ListSeparator) + "/missing"
	want := store + string(filepath.ListSeparator) + "relative" + string(filepath.ListSeparator) + "/missing"
	if got := VisiblePath(input); got != want {
		t.Fatalf("VisiblePath = %q, want %q", got, want)
	}
}

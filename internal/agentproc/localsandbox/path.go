package localsandbox

import (
	"path/filepath"
	"strings"
)

// VisiblePath resolves profile-directory symlinks before /run and home are
// masked. On NixOS these point into the already read-only /nix/store; keeping
// only /run/current-system/sw/bin would leave even cat and sleep unavailable.
// This restores no mounts: directories that resolve under a mask stay hidden.
func VisiblePath(value string) string {
	entries := filepath.SplitList(value)
	for i, entry := range entries {
		if filepath.IsAbs(entry) {
			if resolved, err := filepath.EvalSymlinks(entry); err == nil {
				entries[i] = resolved
			}
		}
	}
	return strings.Join(entries, string(filepath.ListSeparator))
}

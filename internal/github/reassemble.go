package github

import (
	"fmt"
	"strings"
)

// IsBinaryFile heuristically classifies a changed file as binary. GitHub
// omits the patch field for both binary files and oversized/truncated text
// diffs, so an empty patch alone isn't decisive — but a binary file also
// reports zero line additions/deletions, whereas an oversized text file still
// reports its real counts. A rename with no content change likewise has an
// empty patch and zero counts but isn't binary, so it's excluded.
//
// Known limitation: a binary file that is *also* renamed is indistinguishable
// from a pure (content-unchanged) rename via the files API — both have an
// empty patch and zero counts — so a binary rename is reported false. Binary
// renames are rare and ReassemblePRDiff still emits sensible rename headers for
// them; the API exposes no flag that would let us do better.
func IsBinaryFile(f PRFile) bool {
	return f.Patch == "" && f.Additions == 0 && f.Deletions == 0 && f.Status != "renamed"
}

// ReassemblePRDiff synthesizes a unified diff from per-file patches — the
// fallback used when GitHub refuses the verbatim diff media type as too large
// (HTTP 406). GitHub's per-file patch field carries only the @@ hunks, not the
// diff --git / --- / +++ headers, so we prepend minimal headers to keep the
// output a recognizable unified diff. It's approximate but greppable and good
// enough for review. Patch-less files get a "Binary files differ" line when
// they look binary, "rename from/to" headers when they're a rename, or a note
// that the patch was omitted (too large) otherwise — never a fabricated hunk.
func ReassemblePRDiff(files []PRFile) string {
	var b strings.Builder
	for _, f := range files {
		prev := f.PreviousFilename
		if prev == "" {
			prev = f.Filename
		}
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n", prev, f.Filename)
		if f.Patch == "" {
			switch {
			case IsBinaryFile(f):
				fmt.Fprintf(&b, "Binary files a/%s and b/%s differ\n", prev, f.Filename)
			case f.Status == "renamed":
				// A rename with no textual patch is either a pure rename or a
				// binary rename — the files API doesn't distinguish them. Emit
				// rename headers, correct for the pure case and not misleading
				// for the binary one, rather than a bogus "diff too large" note.
				fmt.Fprintf(&b, "rename from %s\nrename to %s\n", prev, f.Filename)
			default:
				fmt.Fprintf(&b, "# patch unavailable (diff too large to fetch); +%d -%d\n", f.Additions, f.Deletions)
			}
			continue
		}
		switch f.Status {
		case "added":
			b.WriteString("--- /dev/null\n")
			fmt.Fprintf(&b, "+++ b/%s\n", f.Filename)
		case "removed":
			fmt.Fprintf(&b, "--- a/%s\n", prev)
			b.WriteString("+++ /dev/null\n")
		default:
			fmt.Fprintf(&b, "--- a/%s\n", prev)
			fmt.Fprintf(&b, "+++ b/%s\n", f.Filename)
		}
		b.WriteString(f.Patch)
		if !strings.HasSuffix(f.Patch, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// SingleFileDiff returns the synthesized diff for one file from a per-file
// patch listing, or "" if the path isn't in the listing. It's the single-file
// HTTP-406 fallback — same reassembly as the full diff, scoped to one file.
func SingleFileDiff(files []PRFile, path string) string {
	for _, f := range files {
		if f.Filename == path {
			return ReassemblePRDiff([]PRFile{f})
		}
	}
	return ""
}

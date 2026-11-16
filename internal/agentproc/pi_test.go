package agentproc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sky-ai-eng/triage-factory/internal/agentproc/localsandbox"
	"github.com/sky-ai-eng/triage-factory/internal/paths"
)

func TestPiPackageRootOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TF_PI_SDK_PATH", dir)
	root, err := piPackageRoot()
	if err != nil || root != dir {
		t.Fatalf("root = %q, %v", root, err)
	}
}

func TestRuntimeWrapperUnknown(t *testing.T) {
	t.Setenv("TF_AGENT_RUNTIME", "typo")
	if _, err := runtimeWrapper(context.Background()); err == nil {
		t.Fatal("unknown runtime accepted")
	}
}

func TestPiRuntimeWrapper(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TF_AGENT_RUNTIME", "pi")
	t.Setenv("TF_PI_SDK_PATH", dir)
	t.Setenv("TF_TOOLCHAIN_ROOT", t.TempDir())
	if _, err := runtimeWrapper(context.Background()); err == nil {
		t.Fatal("missing SDK accepted")
	}
	if err := os.MkdirAll(filepath.Join(dir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dist", "index.js"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	wrapper, err := runtimeWrapper(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(wrapper)
	if err != nil || string(data) != string(piWrapper) {
		t.Fatalf("embedded wrapper mismatch: %v", err)
	}
}

func TestPiAgentDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, tc := range []struct{ value, want string }{
		{"", filepath.Join(home, ".pi", "agent")},
		{"~", home},
		{"~/custom-pi", filepath.Join(home, "custom-pi")},
		{home, home},
	} {
		t.Setenv("PI_CODING_AGENT_DIR", tc.value)
		got, err := piAgentDir()
		if err != nil || got != tc.want {
			t.Fatalf("piAgentDir(%q) = %q, %v; want %q", tc.value, got, err, tc.want)
		}
	}
}

func TestPiSandboxMounts(t *testing.T) {
	t.Setenv("TF_AGENT_RUNTIME", "pi")
	root := t.TempDir()
	paths.SetForTest(t, root)
	stubBwrap(t)
	sdk := filepath.Join(root, "node_modules", "@earendil-works", "pi-coding-agent")
	t.Setenv("TF_PI_SDK_PATH", sdk)
	config := filepath.Join(root, "pi-config")
	t.Setenv("PI_CODING_AGENT_DIR", config)
	spec := sandboxSpec(t, root)
	argv, err := directArgv(RunOptions{Cwd: root, LocalSandbox: spec}, []string{"wrapper.mjs"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasBind(argv, "--bind-try", filepath.Join(config, "auth.json")) {
		t.Fatalf("Pi auth file is not writable: %v", argv)
	}
	for _, path := range []string{sdk, filepath.Join(root, "node_modules"), filepath.Join(config, "models.json"), filepath.Join(config, "models-store.json")} {
		if !hasBind(argv, "--ro-bind-try", path) {
			t.Errorf("missing read-only bind for %s", path)
		}
	}
	if slices.Contains(argv, spec.ClaudeProjectDir) || hasBind(argv, "--bind-try", filepath.Join(os.Getenv("HOME"), ".claude", ".credentials.json")) {
		t.Fatal("Pi sandbox exposes Claude credentials or transcripts")
	}
	if spec.SkipClaude || len(spec.ReadOnly) != 0 {
		t.Fatal("spawn mutated the caller's sandbox plan")
	}
}

// Startup only: no credentials or model requests, but real SDK and bubblewrap.
func TestPiSandboxSDKStartup(t *testing.T) {
	if err := localsandbox.Probe(); err != nil {
		t.Skipf("bubblewrap unavailable: %v", err)
	}
	root, err := piPackageRoot()
	if err != nil {
		t.Skipf("Pi not installed: %v", err)
	}
	t.Setenv("TF_AGENT_RUNTIME", "pi")
	t.Setenv("TF_PI_SDK_PATH", root)
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	t.Setenv("PI_OFFLINE", "1")
	t.Setenv("TF_PI_MODEL", "")
	paths.SetForTest(t, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	wrapper, err := runtimeWrapper(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	cmd, err := newDirectCommand(ctx, RunOptions{Cwd: cwd, LocalSandbox: sandboxSpec(t, cwd)}, []string{wrapper, "--input-format", "stream-json"})
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdin = strings.NewReader("{\"kind\":\"end\"}\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandboxed Pi startup: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), `"subtype":"ready"`) || !strings.Contains(string(out), `"session_id":"pi:`) {
		t.Fatalf("missing protocol handshake: %s", out)
	}
}

func TestPiWrapperProtocol(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	cmd := exec.Command(node, "--test", "pi-wrapper.test.mjs")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Pi wrapper tests: %v\n%s", err, out)
	}
}

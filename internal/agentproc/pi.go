package agentproc

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sky-ai-eng/triage-factory/internal/paths"
)

//go:embed pi-wrapper.mjs
var piWrapper []byte

func piEnabled() bool { return os.Getenv("TF_AGENT_RUNTIME") == "pi" }

func runtimeWrapper(ctx context.Context) (string, error) {
	switch os.Getenv("TF_AGENT_RUNTIME") {
	case "", "claude":
		return ensureSDKTraced(ctx)
	case "pi":
		root, err := piPackageRoot()
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(filepath.Join(root, "dist", "index.js")); err != nil {
			return "", fmt.Errorf("Pi SDK at %s: %w", root, err)
		}
		dir := paths.SDKDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		wrapper := filepath.Join(dir, "pi-wrapper.mjs")
		_, err = writeFileIfChanged(wrapper, piWrapper, 0o644)
		return wrapper, err
	default:
		return "", fmt.Errorf("unknown TF_AGENT_RUNTIME %q (expected claude or pi)", os.Getenv("TF_AGENT_RUNTIME"))
	}
}

// piAgentDir mirrors Pi's getAgentDir without loading operator settings.
func piAgentDir() (string, error) {
	dir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR"))
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".pi", "agent")
	} else if dir == "~" || strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(dir, "~"), "/"))
	}
	return filepath.Abs(dir)
}

// piToolPaths includes sibling SDK dependencies (npm and Nix layouts), not
// the operator's Pi configuration or executable extensions.
func piToolPaths() ([]string, error) {
	root, err := piPackageRoot()
	if err != nil {
		return nil, err
	}
	tools := []string{root}
	for parent := filepath.Dir(root); parent != filepath.Dir(parent); parent = filepath.Dir(parent) {
		if filepath.Base(parent) == "node_modules" {
			tools = append(tools, parent)
			break
		}
	}
	return tools, nil
}

func piPackageRoot() (string, error) {
	if root := strings.TrimSpace(os.Getenv("TF_PI_SDK_PATH")); root != "" {
		return filepath.Abs(root)
	}
	bin, err := exec.LookPath("pi")
	if err != nil {
		return "", fmt.Errorf("Pi not found: install @earendil-works/pi-coding-agent or set TF_PI_SDK_PATH to its package directory")
	}
	bin, err = filepath.EvalSymlinks(bin)
	if err != nil {
		return "", err
	}
	base := filepath.Dir(filepath.Dir(bin))
	// npm links point at dist/cli.js (or dist/bundle/cli.js). Nix wraps the
	// executable and keeps the package under lib/pi/node_modules instead.
	for _, root := range []string{
		base,
		filepath.Dir(base),
		filepath.Join(base, "lib", "pi", "node_modules", "@earendil-works", "pi-coding-agent"),
	} {
		if _, err := os.Stat(filepath.Join(root, "dist", "index.js")); err == nil {
			return root, nil
		}
	}
	return "", fmt.Errorf("cannot locate Pi SDK from %s; set TF_PI_SDK_PATH to its package directory", bin)
}

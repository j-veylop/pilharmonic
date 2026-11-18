package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLanguageChecksDetectionAndPersistence(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	s := openTestService(t, statePath)
	workspace := t.TempDir()
	must(t, os.WriteFile(filepath.Join(workspace, "package.json"), []byte("{}"), 0o644))
	must(t, os.WriteFile(filepath.Join(workspace, "pyproject.toml"), []byte("[project]\n"), 0o644))

	checks := s.ListLanguageChecks(workspace)
	byID := map[string]LanguageCheckInfo{}
	for _, check := range checks {
		byID[check.ID] = check
	}
	if !byID["node"].Detected || !byID["node"].Enabled || !byID["python"].Detected || byID["go"].Detected {
		t.Fatalf("detection wrong: %+v", checks)
	}
	must(t, s.SetLanguageCheckEnabled("node", false))
	if got := s.ListLanguageChecks(workspace); !containsCheck(got, "node", false) {
		t.Fatalf("toggle not applied: %+v", got)
	}
	must(t, s.Shutdown())

	reopened := openTestService(t, statePath)
	defer func() { _ = reopened.Shutdown() }()
	if got := reopened.ListLanguageChecks(workspace); !containsCheck(got, "node", false) {
		t.Fatalf("toggle not persisted: %+v", got)
	}
	if err := reopened.SetLanguageCheckEnabled("unknown", true); err == nil {
		t.Fatal("unknown language check accepted")
	}
}

func containsCheck(checks []LanguageCheckInfo, id string, enabled bool) bool {
	for _, check := range checks {
		if check.ID == id {
			return check.Enabled == enabled
		}
	}
	return false
}

// fakeTool puts a shell script named name on PATH and returns its directory.
func fakeTool(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" + body + "\n"
	must(t, os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestVerificationDetectsNonGoStack(t *testing.T) {
	workspace := t.TempDir()
	must(t, os.WriteFile(filepath.Join(workspace, "package.json"), []byte("{}"), 0o644))
	must(t, os.WriteFile(filepath.Join(workspace, "app.js"), []byte("console.log(1)\n"), 0o644))
	fakeTool(t, "prettier", "echo app.js")

	report := verificationReport(context.Background(), workspace, []string{"app.js"}, nil)
	if !strings.Contains(report, "Node/TypeScript format") || !strings.Contains(report, "app.js") {
		t.Fatalf("non-Go formatting not verified: %q", report)
	}
}

func TestDiagnosticsHonorsDisabledChecks(t *testing.T) {
	workspace := t.TempDir()
	must(t, os.WriteFile(filepath.Join(workspace, "package.json"), []byte("{}"), 0o644))
	fakeTool(t, "eslint", "echo lint-problem; exit 1")

	enabled := diagnosticsForWorkspace(context.Background(), workspace, nil, nil)
	if !strings.Contains(enabled, "lint-problem") {
		t.Fatalf("diagnostics missed the enabled stack: %q", enabled)
	}
	disabled := newCheckSettings(map[string]bool{"node": true})
	if out := diagnosticsForWorkspace(context.Background(), workspace, nil, disabled); out != "" {
		t.Fatalf("disabled stack still ran: %q", out)
	}
}

func TestRunTestsOnlyUsesPackagesWithTests(t *testing.T) {
	workspace := t.TempDir()
	must(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/t\n\ngo 1.22\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package t\n"), 0o644))
	output := runTests(context.Background(), workspace, checkSpecs[0], []string{"main.go"})
	if output != "" {
		t.Fatalf("package without tests ran a test: %q", output)
	}
}
